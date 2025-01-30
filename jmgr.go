/*-
 * Copyright (c) 2024 peter@libassi.se
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */

package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/janeczku/go-spinner"
	"github.com/jlaffaye/ftp"
	"golang.org/x/term"
	"gopkg.in/yaml.v2"
	"net/url"
)

const version = "0.003" // 2025-01-30

// struct for a new jail
type NewJail struct {
	Name       string
	IP         string
	Iface      string
	InheritIP  bool
	IPconf     string
	Dataset    string
	Path       string
	ConfigPath string
}

// struct for a existing jail
type Jail struct {
	Jid         int    `json:"jid"`
	Hostname    string `json:"hostname"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Cpusetid    int    `json:"cpusetid"`
	Path        string `json:"path"`
	Dataset     string `json:"dataset"`
	ConfigPath  string `json:"configpath"`
	OsVersion   string `json:"osversion"`
	OnBoot      string `json:"onboot"`
	Iface       string `json:"iface"`
	Ipv4        string `json:"ipv4"`
	Ipv4Inherit string `json:"ipv4inherit"`
	isParent    bool
	Parent      string   `json:"parent"`
	Ipv4_addrs  []string `json:"ipv4_addrs"`
	Ipv6_addrs  []string `json:"ipv6_addrs"`
	Snapshots   []string `json:"snapshots"`
}

// jls(8) json struct
type JailSlices struct {
	JailSlices []Jail `json:"jail"`
}

// jls(8) json struct
type Jls struct {
	Version string     `json:"__version"`
	Jls     JailSlices `json:"jail-information"`
}

// Config struct for jmgr
type Jmgr struct {
	JmgrConfig       string `json:"jmgrconfig"`                   // Name of jmgr config (YAML) file.
	JailsHome        string `yaml:"JailsHome" json:"jailshome"`   // Directory where new jails are created/cloned
	OsMediaDir       string `yaml:"OsMediaDir" json:"osmediadir"` // Directory where the OS bits are stored
	ZFSdataSet       string `yaml:"ZFSdataSet" json:"zfsdataset"` // if defined JailsHome is derived from ZFSdataSet
	useZFS           bool   // set by jmgrInit()
	badConfig        bool   // set by jmgrInit() to indicate that we do not have resources to create or clone new jails
	JailsConfD       string `json:"jailsconfd"`                               // /etc/jail.conf.d
	JailConfTemplate string `yaml:"JailConfTemplate" json:"jailconftemplate"` // Default: jail.conf.template
	PostInstall      string `yaml:"PostInstall" json:"postinstall"`           // Script if exist runs after create
	OsUrlPrefix      string `yaml:"OsUrlPrefix" json:"osurlprefix"`           // OS download URL prefix
	JailUser         string `yaml:"JailUser" json:"jailuser"`                 // Default user when enter a running jail
	JailIface        string `yaml:"JailIface" json:"jailiface"`               // Default IPv4 interface
	Jails            []Jail `json:"jails"`
}

// interface for register and consume providers of type CLI methods
type Provider interface{ Run([]string) }

// subcommand -> provider map
var SubC = map[string]Provider{
	"config":   ShowStruct{},
	"enable":   EnableDisable{},
	"disable":  EnableDisable{},
	"enter":    Enter{},
	"start":    StartStop{},
	"stop":     StartStop{},
	"restart":  StartStop{},
	"create":   Create{},
	"clone":    Clone{},
	"jails":    ShowJails{},
	"jail":     ShowJails{},
	"runs":     ShowJails{},
	"destroy":  Destroy{},
	"update":   Update{},
	"version":  Version{},
	"snapshot": Snapshot{},
	"rollback": Rollback{},
	"subc":     ProviderMap{},
}

//
// Main
//

func main() {

	log.SetFlags(0) // Remove time and date

	args := os.Args[1:]
	if len(args) == 0 {
		var s ShowJails
		s.Run([]string{"jails"})

	} else {
		// Try if 'subcommand' resolve to a method that is registered as a provider, if so call it.
		v := reflect.ValueOf(SubC[args[0]])
		if v.IsValid() {
			SubC[args[0]].Run(args)
			os.Exit(0)
		}

		// ok, maybe args[0] is a 'jail name', if so call showJails
		cfg := jmgrInit()
		if cfg.exist(args[0]) {
			showJail(&cfg, []string{"jail", args[0]})
			os.Exit(0)
		}
		// We still here?
		help()
	}
}

//
// CLI methods, adheres to the 'Provider' interface
//

// Version emits current software version
type Version struct{}

func (Version) Run(args []string) {
	fmt.Println(version)
}

// Show info from the Jmgr struct
type ShowStruct struct{}

func (ShowStruct) Run(args []string) {

	var cfg Jmgr = jmgrInit()

	jflag := flag.NewFlagSet("config", flag.ExitOnError)
	wantJson := jflag.Bool("json", false, "Print config and all jails in JSON format")
	jflag.Parse(os.Args[2:])

	if *wantJson {
		b, err := json.Marshal(cfg)
		if err != nil {
			log.Fatalln("Problem with JSON encode:" + err.Error())
		}
		fmt.Println(string(b[:]))

	} else {
		var rowsFmt string = "%s\t=\t%s\n"
		var rowsFmtBool string = "%s\t=\t%v\n"
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		values := reflect.ValueOf(cfg)
		types := values.Type()

		for i := 0; i < values.NumField(); i++ {
			if types.Field(i).Name == "Jails" {
				continue
			}
			if types.Field(i).Type.Kind() == reflect.Bool {
				fmt.Fprintf(w, rowsFmtBool, types.Field(i).Name, values.Field(i))
			} else {
				fmt.Fprintf(w, rowsFmt, types.Field(i).Name, values.Field(i))
			}
		}
		w.Flush()
	}
}

// EnableDisable enable or disable a jail to start on boot
type EnableDisable struct{}

func (EnableDisable) Run(args []string) {

	var sysrc string = "/usr/sbin/sysrc"
	_, jail, err := verifyArgs(2, 1, true, true, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	if len(jail.Parent) > 0 {
		log.Fatalln("Jail " + jail.Name + " is a child of " + jail.Parent + ", Can't continue.")
	}

	switch args[0] {

	case "enable":

		if jail.OnBoot == "No" {

			b, err := runCmd(sysrc, []string{"-n", "jail_enable"})
			if err != nil {
				log.Fatalln("EnableDisable():", err.Error())
			}

			if string(bytes.TrimRight(b, "\n")) != "YES" {
				_, err := runCmd(sysrc, []string{"jail_enable=YES"})
				if err != nil {
					log.Fatalln("EnableDisable():", err.Error())
				}
			}

			_, err = runCmd(sysrc, []string{"jail_list+=" + jail.Name})
			if err != nil {
				log.Fatalln("EnableDisable():", err.Error())
			}
		}

	case "disable":

		if jail.OnBoot == "Yes" {

			_, err := runCmd(sysrc, []string{"jail_list-=" + jail.Name})
			if err != nil {
				log.Fatalln("EnableDisable():", err.Error())
			}
		}
	}
}

// Enter jexec into a running jail, optional 'user name'
type Enter struct{}

func (Enter) Run(args []string) {

	cfg, jail, err := verifyArgs(2, 1, true, true, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	if !jail.runs() {
		log.Fatalln("Jail " + jail.Name + " is not running.")

	}

	if len(args) >= 3 {
		cfg.JailUser = args[2]
	}

	cmd := exec.Command("/usr/sbin/jexec", []string{jail.Name, "login", "-f", cfg.JailUser}...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err = cmd.Run()
	if err != nil {
		log.Fatalln("Command finished with error:" + err.Error())
	}
}

// Create a new thick jail
type Create struct{}

func (Create) Run(args []string) {

	cset := flag.NewFlagSet("create", flag.ExitOnError)
	force := cset.Bool("f", false, "Create jail without prompting for confirmation.")
	version := cset.String("v", "", "Freebsd Release, ex: 13.4-RELEASE, if not defined jail is created with host release.")
	list := cset.Bool("l", false, "List available releases")

	cset.Parse(args[1:])
	args = cset.Args()

	if *list {
		err := printRel()
		if err != nil {
			log.Fatalln("Update() get avaliable releases failed: ", err.Error())
		}
		os.Exit(0)
	}

	cfg, _, err := verifyArgs(1, 0, true, false, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	if cfg.badConfig {
		log.Fatalln("jmgr config is not ok. run 'jmgr config' to see the problems reported.")
	}

	// check if we can create a new jail with user input
	newJail, err := cfg.newJailCheck(force, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	var osVersion string
	if len(*version) > 1 {
		osVersion = *version
	} else {
		osVersion, err = hostVersion()
		if err != nil {
			log.Fatalln("Create(): " + err.Error())
		}
	}

	// Good to go.
	fmt.Println("Jail Name:", newJail.Name)
	if newJail.InheritIP {
		fmt.Println("Jail IP: Inherit host IP address")
	} else {
		fmt.Println("Jail IP:", newJail.IP)
		fmt.Println("Jail Iface:", newJail.Iface)
	}
	fmt.Println("os version: ", osVersion)

	if !*force {
		askExitOnNo("Create this jail(yes/No)? ")
	}

	osBits := cfg.OsMediaDir + "/" + osVersion + ".txz"

	if _, err := os.Stat(cfg.OsMediaDir); os.IsNotExist(err) {
		// create media dir
		err := os.MkdirAll(cfg.OsMediaDir, 0755)
		if err != nil {
			log.Fatalln("Error creating directory", err.Error())
		}
	}

	if f, err := os.Stat(osBits); os.IsNotExist(err) || f.Size() < 1 {

		hw, err := machine()
		if err != nil {
			log.Fatalln(err.Error())
		}
		bitsURL := cfg.OsUrlPrefix + "/" + hw + "/" + osVersion + "/base.txz"

		// Download
		s := spinner.StartNew("Downloading FreeBSD: " + bitsURL)
		_, err = runCmd("/usr/bin/fetch", []string{"-q", "-o", osBits, bitsURL})
		if err != nil {
			log.Fatalln("Create() fetch ", err.Error())
		}
		s.Stop()
		fmt.Println("/ Download completed.")
	}

	if cfg.useZFS {
		// create Jail dataset
		_, err = runCmd("/sbin/zfs", []string{"create", newJail.Dataset})
		if err != nil {
			log.Fatalln("Create dataset: " + err.Error())
		}

		// get path for new dataset, remove new line
		b, err := runCmd("/sbin/zfs", []string{"list", "-H", "-o", "mountpoint", newJail.Dataset})
		if err != nil {
			log.Fatalln("Create,zfs list ", err.Error())
		}
		ret := strings.Split(string(b[:]), "\n")
		newJail.Path = ret[0]

		//Just checking
		if len(newJail.Path) == 0 || len(newJail.Dataset) == 0 {
			log.Fatalln("There is a problem. have dataset: " + newJail.Dataset + ", filesystem: " + newJail.Path)
		}
	} else {
		newJail.Path = cfg.JailsHome + "/" + newJail.Name
		err := os.MkdirAll(newJail.Path, 0755)
		if err != nil {
			log.Fatalln("Error creating directory", err.Error())
		}
	}

	// unpack OS bits to new jail dir
	s2 := spinner.StartNew("Unpack " + osBits + " to " + newJail.Path)
	_, err = runCmd("/usr/bin/tar", []string{"-xf", osBits, "-C", newJail.Path})
	if err != nil {
		log.Fatalln("Create() unpack ", err.Error())
	}
	s2.Stop()
	fmt.Println("/ Unpack completed.")

	cfg.createJailConfig(newJail)

	// run postinstall script
	if len(cfg.PostInstall) > 0 {
		fmt.Println("Running Postinstall script:" + cfg.PostInstall)
		p, err := os.Stat(cfg.PostInstall)
		if err != nil {
			log.Fatalln("Error with ", cfg.PostInstall, err.Error())
		} else {
			pMode := p.Mode()
			if pMode.IsRegular() && (pMode.Perm()&0111) > 0 {
				cmd := exec.Command(cfg.PostInstall, []string{newJail.Name, newJail.Path, newJail.ConfigPath}...)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				cmd.Stdin = os.Stdin
				err := cmd.Run()
				if err != nil {
					log.Fatalln("Script " + cfg.PostInstall + " finished with error:" + err.Error())
				}
			} else {
				log.Fatalln("PostInstall script: " + cfg.PostInstall + " is not a file and/or not executable.")
			}
		}
		fmt.Println("Postinstall script completed.")
	}
	fmt.Println("Jail", newJail.Name, "created.")
}

// Clone a existing jail to a new jail
type Clone struct{}

func (Clone) Run(args []string) {

	fset := flag.NewFlagSet("clone", flag.ExitOnError)
	force := fset.Bool("f", false, "Clone jail without prompting for confirmation.")
	fset.Parse(args[1:])
	args = fset.Args()
	cfg, oldJail, err := verifyArgs(2, 0, true, true, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	if cfg.badConfig {
		log.Fatalln("jmgr config is not ok. run 'jmgr config' to see the problems reported.")
	}

	newJail, err := cfg.newJailCheck(force, args[1:])
	if err != nil {
		log.Fatalln(err.Error())
	}

	// Good to go.
	fmt.Println("Jail Name:", newJail.Name)
	if newJail.InheritIP {
		fmt.Println("Jail IP: Inherit host IP address")
	} else {
		fmt.Println("Jail IP:", newJail.IP)
		fmt.Println("Jail Iface:", newJail.Iface)
	}

	if !*force {
		askExitOnNo("Clone this jail from " + oldJail.Name + " (yes/No)? ")
	}

	if len(oldJail.Dataset) > 0 {

		// need a fresh snapshot from source jail
		snapshot, err := snapshot(oldJail.Dataset)
		if err != nil {
			log.Fatalln("Clone, ", err.Error())
		}
		// zfs 'clone'
		err = clone(cfg.useZFS, snapshot, newJail.Dataset)
		if err != nil {
			log.Fatalln("Clone, clone()", err.Error())
		}

		// get newJail snapshot
		b, err := runCmd("/sbin/zfs", []string{"list", "-H", "-t", "snapshot", "-o", "name", newJail.Dataset})
		if err != nil {
			log.Fatalln("zfs list ", err.Error())
		}

		snaps := strings.Split(string(b[:]), "\n")
		if len(snaps) > 1 {
			newJailSnapshot := snaps[0]

			// promote new jail snapshot
			_, err = runCmd("/sbin/zfs", []string{"rollback", newJailSnapshot})
			if err != nil {
				log.Fatalln("zfs rollback ", err.Error())
			}

			// destroy new jail snapshot
			_, err = runCmd("/sbin/zfs", []string{"destroy", newJailSnapshot})
			if err != nil {
				log.Fatalln("zfs destroy ", err.Error())
			}
		} else {
			log.Fatalln("Problem with new jail snapshot, can't continue")
		}

	} else {

		if oldJail.runs() {
			if !*force {
				askExitOnNo("Ok to stop " + oldJail.Name + " (yes/No)? ")
			}
			startstop("stop", oldJail)
			if err != nil {
				log.Fatalln(err.Error())
			}
		}

		newJail.Path = cfg.JailsHome + "/" + newJail.Name
		err := os.MkdirAll(newJail.Path, 0755)
		if err != nil {
			log.Fatalln("Error creating directory ", err.Error())
		}

		err = clone(cfg.useZFS, oldJail.Path, newJail.Path)
		if err != nil {
			log.Fatalln(err.Error())
		}
	}

	err = cfg.createJailConfig(newJail)
	if err != nil {
		log.Fatalln(err.Error())
	}

	fmt.Println("Jail", newJail.Name, "created.")
}

// List existing jails
type ShowJails struct{}

func (ShowJails) Run(args []string) {

	var cfg Jmgr = jmgrInit()

	if len(args) == 1 {
		if args[0] == "runs" {
			reportJails(true, &cfg)
		} else if args[0] == "jails" {
			reportJails(false, &cfg)
		}
	}

	if len(args) == 2 {
		showJail(&cfg, args)
	}
}

// Start or Stop a jail
type StartStop struct{}

func (StartStop) Run(args []string) {

	action := args[0]

	fset := flag.NewFlagSet("startstop", flag.ExitOnError)
	all := fset.Bool("all", false, "Start or Stop all jails.")
	fset.Parse(args[1:])
	args = fset.Args()

	if notRoot() {
		log.Fatalln("Need root to start/stop/restart jails.")
	}

	var cfg Jmgr = jmgrInit()

	if *all {
		for _, jail := range cfg.Jails {
			if len(jail.Parent) == 0 {
				err := startstop(action, &jail)
				if err != nil {
					log.Fatalln(err.Error())
				}
			}
		}

	} else {
		for i := range args {
			if cfg.exist(args[i]) {
				jail := cfg.jail(args[i])
				if len(jail.Parent) > 0 {
					fmt.Println(jail.Name + " is a child of " + jail.Parent + ", skipped.")
				} else {
					err := startstop(action, &jail)
					if err != nil {
						log.Fatalln(err.Error())
					}
				}
			} else {
				fmt.Println(args[i], " does not exist.")
			}
		}
	}
}

// Destroy jail or snapshot
type Destroy struct{}

func (Destroy) Run(args []string) {

	fset := flag.NewFlagSet("destroy", flag.ExitOnError)
	force := fset.Bool("f", false, "Destroy jail[s] without prompting for confirmation.")
	recursive := fset.Bool("r", false, "Destroy jail[s] including their snapshots.")
	fset.Parse(args[1:])
	args = fset.Args()

	if len(args) == 0 {
		help()
	}

	if notRoot() {
		log.Fatalln("Need root to destroy a jail or snapshot.")
	}

	cfg := jmgrInit()
	for index := range args {
		target := args[index]
		if cfg.exist(target) {
			jail := cfg.jail(target)

			if len(jail.Parent) > 0 {
				log.Fatalln("Jail " + jail.Name + " is a child of " + jail.Parent + ", Can't continue.")
			}

			if jail.ConfigPath == "/etc/jail.conf" {
				log.Fatalln("Jail configuration is in " + jail.ConfigPath + ". Remove this jail manually.")
			}

			if !*force {
				fmt.Println("Jail Name:", jail.Name)
				fmt.Println("Jail config:", jail.ConfigPath)
				fmt.Println("Jail Filesystem:", jail.Path)
				if len(jail.Dataset) > 0 {
					fmt.Println("Jail Dataset:", jail.Dataset)
				}
				if jail.isParent {
					fmt.Println("Jail has running jail childs, that also (most likely) will be destroyed.")
				}

				askExitOnNo("Destroy this jail (yes/No)? ")
			}

			if jail.runs() {
				err := startstop("stop", &jail)
				if err != nil {
					log.Fatalln(err.Error())
				}

				time.Sleep(500 * time.Millisecond)
			}

			if len(jail.Dataset) > 0 {
				if *recursive {
					cmd := exec.Command("/sbin/zfs", []string{"destroy", "-r", "-f", jail.Dataset}...)
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					cmd.Stdin = os.Stdin
					err := cmd.Run()
					if err != nil {
						fmt.Println("Error:", err)
					}

				} else {
					// does jail have snapshot(s) ?
					b, err := runCmd("/sbin/zfs", []string{"list", "-H", "-t", "snapshot", "-o", "name", jail.Dataset})
					if err != nil {
						log.Fatalln(err.Error())
					}

					snaps := strings.Split(string(b[:]), "\n")
					if len(snaps) > 1 {
						log.Fatalln("Jail" + jail.Name + " has snapshot(s). Please destroy all snapshots before continue or use '-r'")
					}

					cmd := exec.Command("/sbin/zfs", []string{"destroy", jail.Dataset}...)
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					cmd.Stdin = os.Stdin
					err = cmd.Run()
					if err != nil {
						log.Fatalln(err.Error())
					}

				}
			} else {

				_, err := runCmd("/bin/chflags", []string{"-R", "0", jail.Path})
				if err != nil {
					log.Fatalln(err.Error())
				}

				runCmd("/bin/rm", []string{"-rf", jail.Path})
				if err != nil {
					log.Fatalln(err.Error())
				}

			}

			if jail.OnBoot == "Yes" {
				var d EnableDisable
				d.Run([]string{"disable", jail.Name})
			}

			_, err := runCmd("/bin/rm", []string{jail.ConfigPath})
			if err != nil {
				log.Fatalln("Destroy():", err.Error())
			}

		} else {

			rgx := regexp.MustCompile(".*@.*")
			match := rgx.FindStringSubmatch(target)
			if match == nil {
				log.Fatalln("Name: " + target + " is not a jail or snapshot.")
			}

			cmd := exec.Command("/sbin/zfs", "list", target)
			_, err := cmd.Output()
			if err != nil {
				log.Fatalln("Can't find snapshot: " + target)
			}

			fmt.Println("Snapshot:", target)
			if !*force {
				askExitOnNo("Destroy this snapshot (yes/No)? ")
			}

			_, err = runCmd("/sbin/zfs", []string{"destroy", target})
			if err != nil {
				log.Fatalln(err.Error())
			}
		}
	}
}

// Create a snapshot for dataset
type Snapshot struct{}

func (Snapshot) Run(args []string) {

	_, jail, err := verifyArgs(2, 1, true, true, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	if len(jail.Parent) > 0 {
		log.Fatalln("Jail " + jail.Name + " is a child of " + jail.Parent + ", Can't continue.")
	}

	if len(jail.Dataset) > 0 {
		_, err = snapshot(jail.Dataset)
		if err != nil {
			log.Fatalln(err.Error())
		}
	} else {
		log.Fatalln("Jail", jail.Name, "does not support zfs snapshot.")
	}
}

// Rollback jail to a given snapshot
type Rollback struct{}

func (Rollback) Run(args []string) {

	_, jail, err := verifyArgs(3, 1, true, true, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	if len(jail.Parent) > 0 {
		log.Fatalln("Jail " + jail.Name + " is a child of " + jail.Parent + ", Can't continue.")
	}

	snapshot := args[2]
	latestSnap, err := latestSnapshot(jail.Dataset)
	if err != nil {
		log.Fatalln("No snapshots found for jail " + jail.Name + ", can't continue.")
	}

	if snapshot != latestSnap {
		log.Fatalln("Snapshot: " + snapshot + " is not the latest snapshot for this jail.\nSee 'jmgr " + jail.Name + "', use 'jmgr destroy snapshot'.")
	}

	askExitOnNo("Rollback jail: " + jail.Name + " to snapshot: " + snapshot + " (yes/No)? ")

	if jail.runs() {

		askExitOnNo("Jail is running, stop" + jail.Name + "(yes/No)? ")
		startstop("stop", jail)
	}

	_, err = runCmd("/sbin/zfs", []string{"rollback", snapshot})
	if err != nil {
		log.Fatalln(err.Error())
	}
}

// freebsd update os || upgrade pkgs || upgrade freebsd release
type Update struct{}

func (Update) Run(args []string) {

	fset := flag.NewFlagSet("update", flag.ExitOnError)
	force := fset.Bool("f", false, "Update jail without prompting for confirmation.")
	list := fset.Bool("l", false, "List available releases")
	version := fset.String("v", "", "Freebsd Release, ex: 13.4-RELEASE, if not defined jail is created with host release.")
	fset.Parse(args[1:])
	args = fset.Args()

	if *list {
		err := printRel()
		if err != nil {
			log.Fatalln("Update() get avaliable releases failed: ", err.Error())
		}
		os.Exit(0)
	}

	_, jail, err := verifyArgs(2, 1, true, true, args)
	if err != nil {
		log.Fatalln(err.Error())
	}

	if len(jail.Parent) > 0 {
		log.Fatalln("Jail " + jail.Name + " is a child of " + jail.Parent + ", Can't continue.")
	}

	switch args[0] {

	case "patch":

		if !*force {
			askExitOnNo("Update FreeBSD on: " + jail.Name + ", filesystem: " + jail.Path + ", ZFS dataset: " + jail.Dataset + " (yes/No)?")
		}

		if len(jail.Dataset) > 0 {
			if *force || askYes("Create snapshot before continue (yes/No)?") {
				_, err := snapshot(jail.Dataset)
				if err != nil {
					log.Fatalln("Update() patch snapshot fail:", err.Error())
				}
			}
		}

		err := updateOs(jail)
		if err != nil {
			log.Fatalln("Patch update failed: ", err.Error())
		}
		fmt.Println("/ Update FreeBSD on jail " + jail.Name + " completed.")

	case "rel":

		var osVersion string
		if len(*version) > 1 {
			osVersion = *version
		} else {
			osVersion, err = hostVersion()
			if err != nil {
				log.Fatalln("Create(): " + err.Error())
			}
		}

		rgx := regexp.MustCompile(osVersion)
		match := rgx.FindStringSubmatch(jail.OsVersion)
		if len(match) > 0 {
			log.Fatalln(jail.Name, "already at release", osVersion)
		}

		askExitOnNo("Upgrade " + jail.Name + " FreeBSD from: " + jail.OsVersion + " to: " + osVersion + " (yes/No)?")

		if len(jail.Dataset) > 0 {
			if askYes("Create snapshot before continue (yes/No)?") {
				snapshot(jail.Dataset)
			}
		}

		err := upgradeRel(jail, osVersion)
		if err != nil {
			log.Fatalln("Upgrade Release failed: ", err.Error())
		}
		fmt.Println("FreeBSD upgrade completed.")

	case "pkgs":

		if !*force {
			askExitOnNo("Upgrade all installed packages on: " + jail.Name + " (yes/No)?")
		}

		if jail.Jid == 0 {
			if !*force {
				askExitOnNo("Start (needed for pkg update) " + jail.Name + " (yes/No)?")
			}

			err := startstop("start", jail)
			if err != nil {
				log.Fatalln("Upgrade Pkgs: %w", err)
			}
		}

		if len(jail.Dataset) > 1 {

			if *force || askYes("Create snapshot before continue (yes/No)?") {
				s, err := snapshot(jail.Dataset)
				if err != nil {
					log.Fatalln("Update pkgs Snapshot fail:", err.Error())
				} else {
					fmt.Println("Snapshot: ", s, " Created.")
				}
			}
		}

		err := upgradePkg(jail)
		if err != nil {
			fmt.Println("upgradePkg() returned:", err.Error())
		}

	default:
		help()
	}
}

// ProviderMap dumps the contents of the provider map SubC
type ProviderMap struct{}

func (ProviderMap) Run(_ []string) {

	var f string = "%s\t%s\n"
	var keys []string

	for k := range SubC {
		keys = append(keys, k)
	}

	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, f, "Subcommand", "Method")
	for _, k := range keys {
		fmt.Fprintf(w, f, k, reflect.TypeOf(SubC[k]).String())
	}
	w.Flush()
}

//
// helper methods for struct Jmgr
//

// Jmgr struct method to find and return a Jail struct from the array(slices) of jails
func (cfg *Jmgr) jail(jailname string) Jail {

	for _, jail := range cfg.Jails {
		if jail.Name == jailname {
			return jail
		}
	}
	return Jail{}
}

// Jmgr struct method to check if the jail name already exist in the jails struct
func (cfg *Jmgr) exist(name string) bool {

	if index := slices.IndexFunc(cfg.Jails, func(j Jail) bool { return j.Name == name }); index >= 0 {
		return true
	}
	return false
}

// Jmgr struct method to get index of a existing jail.
func (cfg *Jmgr) jIndex(name string) int {

	if index := slices.IndexFunc(cfg.Jails, func(j Jail) bool { return j.Name == name }); index >= 0 {
		return index
	}
	return -42
}

// createJailConfig Create new /etc/jail.conf.d/<jail.conf> file from template
func (cfg *Jmgr) createJailConfig(newJail NewJail) error {

	if newJail.InheritIP {
		newJail.IPconf = "ip4 = inherit;"
	} else {
		newJail.IPconf = "ip4.addr =  " + newJail.IP + ";\n\tinterface = " + newJail.Iface + ";"
	}
	sed := strings.NewReplacer(
		"<JailName>", newJail.Name,
		"<JailPath>", cfg.JailsHome+"/"+newJail.Name,
		"<IPConf>", newJail.IPconf,
	)

	// Load template
	Template, err := os.ReadFile(cfg.JailConfTemplate)
	if err != nil {
		return fmt.Errorf("can't open jail config template file %s error: %s", cfg.JailConfTemplate, err.Error())
	}

	TemplateStr := string(Template) // bytes -> string
	NewConfStr := sed.Replace(TemplateStr)

	if err = os.WriteFile(newJail.ConfigPath, []byte(NewConfStr), 0666); err != nil {
		return fmt.Errorf("write to %s, %s", newJail.ConfigPath, err.Error())
	}

	return nil
}

// jmgrConfigfileReader method to read YAML config file
func (cfg *Jmgr) jmgrConfigfileReader() {

	s, err := os.Stat(cfg.JmgrConfig)
	if err != nil {
		cfg.JmgrConfig = "File '" + cfg.JmgrConfig + "' does not exist."
		cfg.badConfig = true
		return
	}
	if s.IsDir() {
		cfg.JmgrConfig = "File '" + cfg.JmgrConfig + "' is a directory."
		cfg.badConfig = true
		return
	}

	// read file
	file, err := os.Open(cfg.JmgrConfig)
	if err != nil {
		cfg.JmgrConfig = "File '" + cfg.JmgrConfig + "' Gives error:" + err.Error()
		cfg.badConfig = true
		return
	}
	defer file.Close()

	d := yaml.NewDecoder(file)
	if err := d.Decode(&cfg); err != nil {
		cfg.JmgrConfig = cfg.JmgrConfig + " Problem decoding."
		cfg.badConfig = true
		return
	}
}

// addJails method goes out and harvest info about existing jails and add these to the Jmgr struct
func (cfg *Jmgr) addJails() {

	// expressions to capture the jail conf syntax
	rgx := make(map[string]*regexp.Regexp)
	rgx["name"] = regexp.MustCompile(`(.*)\s+{`)
	rgx["Ipv4"] = regexp.MustCompile(`ip4\.addr.=\s*(\d+\.\d+\.\d+\.\d+);`)
	rgx["Ipv4Inherit"] = regexp.MustCompile(`ip4\s+=\s+(\w+);`)
	rgx["Path"] = regexp.MustCompile(`path.=\s*"(.*)";`)
	rgx["Hostname"] = regexp.MustCompile(`hostname\s?=\s?(?P<Hostname>.*);`)
	rgx["end"] = regexp.MustCompile(`}`)

	b, err := runCmd("/usr/sbin/jls", []string{"-v", "--libxo", "json"})
	if err != nil {
		fmt.Println("addJails() -> jls: " + err.Error())
	}

	var f Jls
	err = json.Unmarshal(b, &f)
	if err != nil {
		fmt.Println("addJails() -> json: " + err.Error())
	}

	// extract the interesting part of the JSON jls struct
	cfg.Jails = append(cfg.Jails, f.Jls.JailSlices...)

	// Find jails in /etc/jail.conf.d/*.conf
	files, err := os.ReadDir(cfg.JailsConfD)
	if err == nil {
		for _, f := range files {
			if strings.Contains(f.Name(), ".conf") {
				cfg.addJailDetailsFromFile(cfg.JailsConfD+"/"+f.Name(), rgx)
			}
		}
	}

	// and the jail.conf
	cfg.addJailDetailsFromFile("/etc/jail.conf", rgx)

	// get jails that start on boot
	jailList, err := runCmd("/usr/sbin/sysrc", []string{"-n", "jail_list"})
	if err != nil {
		fmt.Println("addJails() -> sysrc:", err.Error())
	}
	// Add more details to all jails
	for i := 0; i < len(cfg.Jails); i++ {

		// add start on boot
		cfg.Jails[i].OnBoot = inJailList(jailList, cfg.Jails[i].Name)

		// add ZFS dataset
		if len(cfg.Jails[i].Path) > 0 {
			p, err := os.Stat(cfg.Jails[i].Path)
			if err == nil {
				if p.IsDir() {
					b, err := runCmd("/sbin/zfs", []string{"list", "-H", cfg.Jails[i].Path})
					if err == nil {
						words := strings.Fields(string(b[:]))
						if len(words) > 0 {
							regx := regexp.MustCompile(cfg.Jails[i].Name)
							match := regx.FindStringSubmatch(string(words[0]))
							if len(match) > 0 {
								cfg.Jails[i].Dataset = words[0]
								snaps, err := jailSnapshots(cfg.Jails[i].Dataset)
								if err == nil {
									cfg.Jails[i].Snapshots = snaps
								}
							}
						}
					}
				}
			}
		}

		// add jail os version
		v, err := jailVersion(cfg.Jails[i].Path)
		if err == nil {
			cfg.Jails[i].OsVersion = v
		}

		// add IPv4 address from jls Ipv4_addrs array if empty or if defined set it to inherit
		if len(cfg.Jails[i].Ipv4) == 0 && len(cfg.Jails[i].Ipv4_addrs) > 0 {
			cfg.Jails[i].Ipv4 = cfg.Jails[i].Ipv4_addrs[0]

		} else if len(cfg.Jails[i].Ipv4Inherit) > 0 {
			cfg.Jails[i].Ipv4 = cfg.Jails[i].Ipv4Inherit
		}

		// is it a child? family[0] == Parent, family[1] == Child
		if family := strings.Split(cfg.Jails[i].Name, "."); len(family) > 1 {
			if cfg.exist(family[0]) {

				cfg.Jails[cfg.jIndex(family[0])].isParent = true

				// need root to run commands in a jail. Rely on the "." name convention for regular user for now.
				if notRoot() {
					cfg.Jails[i].Parent = family[0]

				} else {
					b, err := runCmd("/usr/sbin/jexec", []string{family[0], "/sbin/sysctl", "-n", "security.jail.children.cur"})
					if err == nil {
						if string(b) != "0" {
							cfg.Jails[i].Parent = family[0]
						}
					} else {
						cfg.Jails[i].Parent = "Can't determine Parent."
					}
				}
			}
		}
	}
}

// add/update jails from /etc/jail.conf & /etc/jail.conf.d/*.conf
func (cfg *Jmgr) addJailDetailsFromFile(file string, rgx map[string]*regexp.Regexp) {

	f, err := os.Open(file)
	if err == nil {
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			match := rgx["name"].FindStringSubmatch(scanner.Text())
			if len(match) > 0 {
				var addJail Jail
				addJail.Name = strings.TrimSpace(match[1])
				addJail.ConfigPath = file

				for scanner.Scan() {
					// found end of jail conf, add info to existing jail struct or add a new jail to the struct
					match := rgx["end"].FindStringSubmatch(scanner.Text())
					if len(match) > 0 {
						if cfg.exist(addJail.Name) {
							for i := 0; i < len(cfg.Jails); i++ {
								if cfg.Jails[i].Name == addJail.Name {
									cfg.Jails[i].Hostname = addJail.Hostname
									cfg.Jails[i].Path = addJail.Path
									cfg.Jails[i].Ipv4 = addJail.Ipv4
									cfg.Jails[i].Ipv4Inherit = addJail.Ipv4Inherit
									cfg.Jails[i].ConfigPath = addJail.ConfigPath
								}
							}
						} else {
							cfg.Jails = append(cfg.Jails, addJail)
						}
						break
					}
					// loop trough all regex, if match update corresponding struct field
					for field := range rgx {
						if field == "name" || field == "end" {
							continue
						}
						match = rgx[field].FindStringSubmatch(scanner.Text())
						if len(match) > 0 {
							reflect.ValueOf(&addJail).Elem().FieldByName(field).Set(reflect.ValueOf(strings.TrimSpace(match[1])))
						}
					}
				}
			}
		}
	}
}

// newJailCheck check Jail create/clone prereqs (jail_name [IP] [Iface])
func (cfg *Jmgr) newJailCheck(force *bool, args []string) (NewJail, error) {

	if cfg.exist(args[0]) {
		return NewJail{}, fmt.Errorf("%s alreay exist", args[0])
	}

	if cfg.useZFS {
		// Sanity check: base cfg.ZFSdataSet exist
		zfsList, err := runCmd("/sbin/zfs", []string{"list", cfg.ZFSdataSet})
		if err != nil {
			return NewJail{}, fmt.Errorf(" %s Does not exist. %s", cfg.ZFSdataSet, string(zfsList))
		}

		// Sanity check: get mount point for base zfs dataset and verify that it matches cfg.JailsHome
		rgx := regexp.MustCompile(cfg.JailsHome)
		match := rgx.FindStringSubmatch(string(zfsList))
		if len(match) == 0 {
			return NewJail{}, fmt.Errorf("jmgr config 'jail home' does no match where %s is mounted", cfg.ZFSdataSet)
		}
	}

	var jail NewJail
	jail.Name = args[0]
	jail.Iface = cfg.JailIface

	// resolve jail name to IP
	addrs, err := net.LookupHost(jail.Name)
	if err == nil {
		jail.IP = addrs[0]

	} else { // IP Address in arg?
		if len(args) > 1 {
			_, _, err := net.ParseCIDR(args[1] + "/24")
			if err != nil {
				return NewJail{}, fmt.Errorf("not a valid IP address: %s", args[1])
			}
			jail.IP = args[1]
		}
	}

	// Do we have an IP now? else ask for inherit
	if len(jail.IP) == 0 {
		if *force {
			jail.InheritIP = true
		} else {
			jail.InheritIP = askExitOnNo("No IP address found. Use host IP (yes/No)? ")
		}
	} else {
		// ping IP
		ping := exec.Command("/sbin/ping", "-c 2", "-t 2", jail.IP)
		_, err = ping.Output()
		if err == nil {
			return NewJail{}, fmt.Errorf("ip address already in use, %s responds to ping, can't continue", jail.IP)
		}

		// Iface in arg
		if len(args) > 2 {
			jail.Iface = args[2]
		}

		ifcnf := exec.Command("/sbin/ifconfig", "-l")
		out, err := ifcnf.Output()
		if err == nil {
			// quick and dirty, we may find more than we want.. it's on the TODO list
			if !bytes.Contains(out, []byte(jail.Iface)) {
				return NewJail{}, fmt.Errorf("can't find interface: %s on this system", jail.Iface)
			}
		} else {
			return NewJail{}, fmt.Errorf("can't check interface: %s", err.Error())
		}
	}

	//Check Config dir
	d, err := os.Stat(cfg.JailsConfD)
	if err != nil {
		return NewJail{}, fmt.Errorf("directory does not exist. Please create %s Then try again", cfg.JailsConfD)
	}
	if !d.IsDir() {
		return NewJail{}, fmt.Errorf("%s is not a directory, can't create new jail", cfg.JailsConfD)
	}

	// if exist /etc/jail.conf.d/<jail.conf>
	jail.ConfigPath = cfg.JailsConfD + "/" + jail.Name + ".conf"

	if _, err := os.Stat(jail.ConfigPath); os.IsExist(err) {
		return NewJail{}, fmt.Errorf("file: %s  Already exist", jail.ConfigPath)
	}

	if cfg.useZFS {
		// Check jails dataset
		jail.Dataset = cfg.ZFSdataSet + "/" + jail.Name

		cmd := exec.Command("/sbin/zfs", "list", jail.Dataset)
		_, err = cmd.Output()
		if err == nil {
			return NewJail{}, fmt.Errorf("already exist ZFS dataset: %s ", jail.Dataset)
		}
	} else {
		// check if jail Path already exist
		jail.Path = cfg.JailsHome + "/" + jail.Name
		_, err := os.Stat(jail.Path)
		if err == nil {
			return NewJail{}, fmt.Errorf("%s already exist", jail.Path)
		}
	}

	return jail, nil
}

//
// helper methods for struct Jail
//

// Jail struct method returning if jail is running or not
func (j *Jail) runs() bool {

	if j.Jid > 0 {
		return true
	} else {
		return false
	}
}

//
// helper functions
//

// Return a populated a Jmgr struct
func jmgrInit() Jmgr {

	var cfg Jmgr

	// init defaults
	cfg.useZFS = false
	cfg.badConfig = false
	cfg.JailsConfD = "/etc/jail.conf.d"

	env, ok := os.LookupEnv("JMGR_CONFIG")
	if len(env) > 0 && ok {
		cfg.JmgrConfig = env
	} else {
		cfg.JmgrConfig = "/usr/local/etc/jmgr/jmgr.conf"
	}

	// populate Jmgr struct from file
	cfg.jmgrConfigfileReader()

	if len(cfg.ZFSdataSet) > 0 {
		cfg.useZFS = true
		cmd := exec.Command("/sbin/zfs", "list", "-H", cfg.ZFSdataSet)
		b, err := cmd.Output()
		if err != nil {
			cfg.ZFSdataSet = "Dataset " + cfg.ZFSdataSet + " does not exist."
			cfg.badConfig = true
		} else {
			words := strings.Fields(string(b[:]))
			if len(words) > 0 {
				cfg.JailsHome = words[4]
			} else {
				cfg.JailsHome = "Can't find Jails Home directory using 'ZFS dataset': " + cfg.ZFSdataSet
				cfg.badConfig = true
			}
		}
	} else {
		if _, err := os.Stat(cfg.JailsHome); os.IsNotExist(err) {
			cfg.JailsHome = cfg.JailsHome + " does not exist."
			cfg.badConfig = true
		}
	}

	// populate struct with existing jails
	cfg.addJails()

	return cfg
}

// showJail
func showJail(cfg *Jmgr, args []string) {

	if cfg.exist(args[1]) {
		var jail = cfg.jail(args[1])
		var rowsFmt string = "%s\t%s\n"

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		jidText := strconv.Itoa(jail.Jid)
		if jail.Jid > 0 {
			jidText = jidText + " (Running)"
		} else {
			jidText = jidText + " (Not running)"
		}

		fmt.Fprintf(w, rowsFmt, "Jid", jidText)
		fmt.Fprintf(w, rowsFmt, "Name", jail.Name)
		fmt.Fprintf(w, rowsFmt, "Hostname", jail.Hostname)
		
		if len(jail.Ipv4_addrs) > 0 {
			for _, ipv4 := range jail.Ipv4_addrs {
				if len(ipv4) > 0 {
					fmt.Fprintf(w, rowsFmt, "IPv4", ipv4)
				}
			}
		} else {
			fmt.Fprintf(w, rowsFmt, "IP Address", jail.Ipv4)
		}

		if len(jail.Iface) > 0 {
			fmt.Fprintf(w, rowsFmt, "Interface", jail.Iface)
		}

		for _, ipv6 := range jail.Ipv6_addrs {
			if len(ipv6) > 0 {
				fmt.Fprintf(w, rowsFmt, "IPv6", ipv6)
			}
		}
		if len(jail.Parent) > 0 {
			fmt.Fprintf(w, rowsFmt, "Parent jail", jail.Parent)
		}
		if jail.isParent {
			fmt.Fprintf(w, rowsFmt, "Jail Parent", "True")
		}
		fmt.Fprintf(w, rowsFmt, "Config", jail.ConfigPath)
		fmt.Fprintf(w, rowsFmt, "OS Version", jail.OsVersion)
		fmt.Fprintf(w, rowsFmt, "Start on boot", jail.OnBoot)
		fmt.Fprintf(w, rowsFmt, "Path", jail.Path)

		if len(jail.Dataset) <= 0 {
			jail.Dataset = "N/A"
		}

		fmt.Fprintf(w, rowsFmt, "ZFS Dataset", jail.Dataset)

		for _, snap := range jail.Snapshots {
			if len(snap) > 0 {
				fmt.Fprintf(w, rowsFmt, "ZFS Snapshot", snap)
			}
		}

		w.Flush()
	}
}

// Check if current user has sufficent capabilites
func notRoot() bool {
	currentUser, err := user.Current()
	if err != nil {
		return false

	} else if currentUser.Uid > "0" {
		return true
	}

	return false
}

// execute command and return it's stdout & stderr
func runCmd(command string, args []string) ([]byte, error) {

	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd := exec.Command(command, args...)
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("%s %s failed with:%s", command, args, stderr.String())
	}
	return stdout.Bytes(), nil
}

// runCmdStdin Interact with running command.
func runCmdStdin(command string, args []string) error {

	cmd := exec.Command(command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// return the hosts FreeBSD version
func hostVersion() (string, error) {

	rgx := regexp.MustCompile(`(.*RELEASE)`)
	b, err := runCmd("/bin/freebsd-version", []string{})
	if err != nil {
		return "", fmt.Errorf("hostVersion() failed with: %w", err)
	}
	match := rgx.FindStringSubmatch(string(b[:]))

	return match[1], nil
}

// return the given jail FreeBSD version
func jailVersion(jailPath string) (string, error) {

	_, err := os.Stat(jailPath)
	if err != nil {
		return "", fmt.Errorf("jailVersion, Path: %s error %w", jailPath, err)
	}

	b, err := runCmd("/usr/bin/env", []string{"ROOT=" + jailPath, jailPath + "/bin/freebsd-version"})
	if err != nil {
		return "", fmt.Errorf("jailVersion failed: %w", err)
	}

	return string(bytes.TrimRight(b, "\n")), nil
}

// Starts, stops or restart a given jail.
func startstop(action string, jail *Jail) error {

	if len(jail.Parent) > 0 {
		return fmt.Errorf("it's a child. Should be managed from %s", jail.Parent)
	}

	var command string = "/usr/sbin/jail"
	var args []string
	rgx := regexp.MustCompile("jail.conf.d")
	match := rgx.FindStringSubmatch(jail.ConfigPath)

	switch action {

	case "start":
		if jail.runs() {
			return nil
		} else {
			if match == nil {
				args = []string{"-c", jail.Name}
			} else {
				args = []string{"-c", "-f", jail.ConfigPath}
			}
		}

	case "stop":
		if !jail.runs() {
			return nil
		} else {
			args = []string{"-r", "-f", jail.ConfigPath, jail.Name}
		}

	case "restart":
		if match == nil {
			args = []string{"-rc", jail.Name}
		} else {
			args = []string{"-rc", "-f", jail.ConfigPath}
		}

	default:
		return errors.New("startstop() does not understand what to do")
	}

	_, err := runCmd(command, args)
	if err != nil {
		return err
	}
	return nil

}

// verifyArgs verify requirements before continue. dies if missing requirements. Returns: false with nil pointers or true with struct pointers.
func verifyArgs(minargs int, namePos int, needRoot bool, exist bool, args []string) (*Jmgr, *Jail, error) {

	if len(args) < minargs || args[namePos] == "help" || args[namePos] == "-h" {
		help()
	}

	if needRoot && notRoot() {
		return nil, nil, errors.New("need root capabilites to perform this task")
	}

	var cfg Jmgr = jmgrInit()
	if exist && !cfg.exist(args[namePos]) {
		return nil, nil, errors.New("Jail " + args[namePos] + " does not exist.")
	}

	var jail Jail = cfg.jail(args[namePos])

	return &cfg, &jail, nil
}

// jailSnapshots return all ZFS snapshots for jail
func jailSnapshots(zfsPath string) ([]string, error) {

	var snaps []string

	b, err := runCmd("/sbin/zfs", []string{"list", "-H", "-t", "snapshot", "-o", "name", zfsPath})
	if err != nil {
		return nil, fmt.Errorf("jailSnapshots() failed: %w", err)
	}

	for _, snap := range strings.Split(string(b[:]), "\n") {
		words := strings.Fields(snap)
		if len(words) > 1 && words[1] == "-" {
			continue
		} else {
			snaps = append(snaps, snap)
		}
	}
	return snaps, nil
}

// inJailList( addJails() helper, just return info if 'Name' exist in sysrc 'jail_list'
func inJailList(jailList []byte, Name string) string {

	rgx := regexp.MustCompile(`\b(` + Name + `)\b`)
	if len(rgx.FindStringSubmatch(string(jailList))) > 1 {
		return "Yes"
	} else {
		return "No"
	}
}

// ask user, exit if not yes
func askExitOnNo(question string) bool {

	fmt.Print(question)
	var answer string
	fmt.Scanln(&answer)
	if strings.ToUpper(answer) == "YES" || strings.ToUpper(answer) == "Y" {
		return true
	}
	os.Exit(0)
	return false // make compiler happy
}

// ask user return true if yes
func askYes(question string) bool {

	fmt.Print(question)
	var answer string
	fmt.Scanln(&answer)
	if strings.ToUpper(answer) == "YES" || strings.ToUpper(answer) == "Y" {
		return true
	}
	return false
}

// create a snapshot
func snapshot(dataset string) (string, error) {

	t := time.Now()
	today := t.Format("2006-01-02T15:04:05")

	sname := dataset + "@" + today
	_, err := runCmd("/sbin/zfs", []string{"snapshot", sname})
	if err != nil {
		return sname, fmt.Errorf("snapshot() failed: %w", err)
	}

	return sname, nil
}

// return latest snapshot for jail
func latestSnapshot(dataset string) (string, error) {

	b, err := runCmd("/sbin/zfs", []string{"list", "-H", "-t", "snapshot", "-o", "name", dataset})
	if err != nil {
		return "", fmt.Errorf("latestSnapshot() failed: %w", err)
	}

	snaps := strings.Split(string(b[:]), "\n")
	if len(snaps) < 2 {
		return "", fmt.Errorf("latestSnapshot() no snapshots found for: %s", dataset)
	}

	return snaps[len(snaps)-2], nil
}

// print out all jails
func reportJails(runs bool, cfg *Jmgr) {

	var labelFmt string = " %s\t%s\t%s\t%s\t%s"
	var rowsFmt string = " %d\t%s\t%s\t%s\t%s"
	var narrow int = 80

	width, _, err := term.GetSize(0)
	if err != nil {
		width = narrow + 1
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	switch {

	case width > narrow:
		labelFmt += "\t%s\t%s\n"
		rowsFmt += "\t%s\t%s\n"
		fmt.Fprintf(w, labelFmt, "Jid", "Name", "IP Address", "Path", "Config", "OS Version", "Boot")

	default:
		labelFmt += "\n"
		rowsFmt += "\n"
		fmt.Fprintf(w, labelFmt, "Jid", "Name", "IP Address", "Path", "OS Version", "Boot")
	}

	// iterate Jails
	for _, jail := range cfg.Jails {
		if runs && jail.Jid == 0 {
			continue
		} else {
			switch {
			case width > narrow:
				fmt.Fprintf(w, rowsFmt, jail.Jid, jail.Name, jail.Ipv4, jail.Path, jail.ConfigPath, jail.OsVersion, jail.OnBoot)
			default:
				fmt.Fprintf(w, rowsFmt, jail.Jid, jail.Name, jail.Ipv4, jail.Path, jail.OsVersion, jail.OnBoot)
			}
		}
	}
	w.Flush()
}

// upgrade packages
func upgradePkg(jail *Jail) error {

	// pkg update
	cmd := exec.Command("/usr/sbin/pkg", []string{"-j", jail.Name, "update"}...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("upgradePkg(): %w", err)
	}

	// pkg upgrade
	cmd = exec.Command("/usr/sbin/pkg", []string{"-j", jail.Name, "upgrade"}...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("upgradePkg(): %w", err)
	}

	return nil
}

// freebsd upgrade jail to a new release
func upgradeRel(jail *Jail, Release string) error {

	// get new release
	err := runCmdStdin("/usr/sbin/freebsd-update", []string{"-b", jail.Path, "--currently-running", jail.OsVersion, "-r", Release, "upgrade"})
	if err != nil {
		return fmt.Errorf("command freebsd-update upgrade finished with error: %w", err)
	}

	// first install
	err = runCmdStdin("/usr/sbin/freebsd-update", []string{"-b", jail.Path, "install"})
	if err != nil {
		return fmt.Errorf("upradeRel install 1: %w", err)
	}

	// jail restart
	err = startstop("stop", jail)
	if err != nil {
		return fmt.Errorf("upgradeRel() stop: %w", err)
	}

	time.Sleep(200 * time.Millisecond)

	err = startstop("start", jail)
	if err != nil {
		return fmt.Errorf("upgradeRel() start: %w", err)
	}

	// second install
	err = runCmdStdin("/usr/sbin/freebsd-update", []string{"-b", jail.Path, "install"})
	if err != nil {
		return fmt.Errorf("upradeRel install 2: %w", err)
	}

	return nil
}

// fetch and print avaliable freebsd releases
func printRel() error {

	var cfg Jmgr = jmgrInit()
	hw, err := machine()
	if err != nil {
		return fmt.Errorf("printRel() failed: %w", err)
	}

	fetchURL := cfg.OsUrlPrefix + "/" + hw + "/" + hw + "/"
	u, err := url.Parse(fetchURL)
	if err != nil {
		return fmt.Errorf("printRel() failed: %w", err)
	}

	c, err := ftp.Dial(u.Hostname()+":21", ftp.DialWithTimeout(5*time.Second))
	if err != nil {
		return fmt.Errorf("printRel() failed: %w", err)
	}
	defer c.Quit()

	err = c.Login("anonymous", "anonymous")
	if err != nil {
		return fmt.Errorf("printRel() failed: %w", err)
	}

	list, err := c.List(u.EscapedPath())
	if err != nil {
		return fmt.Errorf("printRel() failed: %w", err)
	}

	rgx := regexp.MustCompile(`(.*RELEASE)`)
	fmt.Println("Available Releases at:", fetchURL)
	for _, entry := range list {
		match := rgx.FindStringSubmatch(entry.Name)
		if len(match) > 1 {
			fmt.Println(entry.Name)
		}
	}

	return nil
}

// freebsd update to latest patch
func updateOs(jail *Jail) error {

	s := spinner.StartNew("Update FreeBSD on jail " + jail.Name)

	_, err := runCmd("/usr/bin/env", []string{
		"UNAME_r=" + jail.OsVersion,
		"/usr/sbin/freebsd-update", "-b", jail.Path,
		"--currently-running", jail.OsVersion,
		"--not-running-from-cron",
		"fetch", "install"})

	s.Stop()
	if err != nil {
		return fmt.Errorf("runCMD() reports: %s", err.Error())
	}

	return nil
}

// return hw platform
func machine() (string, error) {

	b, err := runCmd("/usr/bin/uname", []string{"-m"})
	if err != nil {
		return "", fmt.Errorf("machine() %s ", err.Error())
	}
	return string(bytes.TrimRight(b, "\n")), nil
}

// ZFS or FS clone with Spinner, 'from'/'to' is either ZFS snapshot/dataset or old/new directory all depending on 'useZFS'
func clone(useZFS bool, from string, to string) error {

	s := spinner.StartNew("Clone " + from + " to " + to)

	var err error
	var RecvOut io.ReadCloser
	var Send, Recv *exec.Cmd

	if useZFS {
		Send = exec.Command("/sbin/zfs", "send", from)
		Recv = exec.Command("/sbin/zfs", "receive", to)
	} else {
		Send = exec.Command("/bin/sh", "-c", "cd "+from+";/usr/bin/tar -cf - *")
		Recv = exec.Command("/usr/bin/tar", "-x", "-C", to)
	}

	Recv.Stdin, err = Send.StdoutPipe()
	if err != nil {
		return fmt.Errorf("clone() Send.StdoutPipe(): %w", err)
	}

	RecvOut, err = Recv.StdoutPipe()
	if err != nil {
		return fmt.Errorf("clone() Recv.StdoutPipe(): %w", err)
	}

	// Start clone
	err = Recv.Start()
	if err != nil {
		return fmt.Errorf("clone() Recv.Start(): %w", err)
	}

	err = Send.Start()
	if err != nil {
		return fmt.Errorf("clone() Send.Start(): %w", err)
	}

	// Read the output of the 'receiver' command
	RecvResult, err := io.ReadAll(RecvOut)
	if err != nil {
		return fmt.Errorf("clone() io.ReadAll: %w", err)
	}

	// Wait for transfer to finish
	err = Send.Wait()
	if err != nil {
		return fmt.Errorf("clone() Send.Wait(): %w", err)
	}

	err = Recv.Wait()
	if err != nil {
		return fmt.Errorf("clone() Recv.Wait(): %w", err)
	}

	s.Stop()
	time.Sleep(200 * time.Millisecond)
	fmt.Println("/ Completed.")

	if len(RecvResult) > 0 {
		fmt.Printf("zfs recv report: %s\n", RecvResult)
		return fmt.Errorf("clone() RecvResult: %s", string(RecvResult))
	}
	return nil
}

// Help page
func help() {

	var string = ` jmgr help

 Syntax: jmgr [ subcommand ] [options] [ arguments.. ] | [ jail name ]
  
 View:
  config [-json]			
  jails  
  runs	
  'jail name'	
										
 Create/Backup:
  create [-f] [-v 'FreeBSD Release'] 'jail name' [ 'IP address' [ 'interface name' ] ]
  create -l 
  snapshot 'jail name'

 Clone:
  clone [-f] 'from jail name' 'new jail name' [ 'new jail IP address' [ 'new jail interface' ] ]

 Jails admin:  			
  enter 'jail name' [ 'user name' ]
  start [-all] ['jail name' 'jail name2' ... ] 
  stop [-all] ['jail name' 'jail name2' ... ] 
  restart [-all] ['jail name' 'jail name2' ... ] 
  enable 'jail name'	
  disable 'jail name'

 Destroy:	
  destroy [-f] [-r ]'jail name'	
  destroy [-f] 'snapshot name'	

 Update os, Upgrade pkgs, Upgrade os release:
  update [-f] patch 'jail name'
  update [-f] pkgs 'jail name'
  update [-v 'FreeBSD Release'] rel 'jail name'
  update -l

 Rollback:
  rollback 'jail name' 'latest snapshot name'

Options:
  -f 		Assume 'yes' on all questions. 
  -json		Print output in JSON format
  -r 		Destroy jail[s] including their snapshots
  -all		Start or Stop all jails.
  -l 		Provides a list of avaliable 'FreeBSD Releases'
  -v		Define desired version of 'FreeBSD Release'

 See jmgr(8) for details.

` // eof string

	fmt.Println(string)
	os.Exit(0)
}

// eof
