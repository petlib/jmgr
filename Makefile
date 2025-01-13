# Define the source, target and the install destinations
TARGET = jmgr
STARGET = jmgr.go
MAIN_PACKAGE_PATH = .
LBIN = /usr/local/bin
JHOME = /usr/local/etc/jmgr
SDIR = ./usr/local/etc/jmgr
MANDIR = /usr/share/man/man8
SMANDIR = ./man8
SMAN = ${SMANDIR}/jmgr.8
SMANZ = ${SMANDIR}/jmgr.8.gz
TARGETS = ${TARGET} ${SMANZ}

# for 'make'
.PHONY: build 
build: $(TARGETS)

# build the CLI
${TARGET}: ${STARGET}
	go build -ldflags "-s" -o=${TARGET} ${MAIN_PACKAGE_PATH}

# gzip man page
${SMANZ}: ${SMAN}
	/usr/bin/gzip -kf ${SMANDIR}/jmgr.8

# new install
.PHONY: install
install:
	/usr/bin/install ${TARGET} ${LBIN}
	/usr/bin/install -d ${JHOME}
	/usr/bin/install ${SDIR}/jmgr.conf ${JHOME}
	/usr/bin/install ${SDIR}/jail.conf.template ${JHOME}
	/usr/bin/install ${SDIR}/postinstall.sh ${JHOME}
	/usr/bin/install ${SMANZ} ${MANDIR}

# Clean target: remove the targets
clean:
	rm -f ${TARGET}
	rm -f ${SMANZ}

# git pull
.PHONY: pull
pull:
	git pull

# pull and build
.PHONY: latest
latest: pull $(TARGETS)

# install latest binary and man page
.PHONY: update
update:
	/usr/bin/install ${TARGET} ${LBIN}
	/usr/bin/install ${SMANDIR}/jmgr.8.gz ${MANDIR}

