#
# Instructions for Install, Test, Run and Remove of the jmgr(8) jail management tool.
#

Install requirements:

    # pkg install git go

Get source:
    $ cd $HOME
    $ git clone gitserver:my_src/jmgr

Build and install:
    $ cd jmgr
    $ make 
    # make install

Create the ZFS dataset home for jails, example: 
    # zfs create -o mountpoint=/usr/local/jails zroot/jails

Check/adjust /usr/local/etc/jmgr/jmgr.conf, especially 'ZFSdataSet'

Start play with jmgr:
    $ man jmgr

Optional test of jmgr ( Creates a test jail named: testJ99 )
    $ cd $HOME/jmgr/test
    # make testall

Remove jmgr
    $ rm -rf $HOME/jmgr
    # rm /usr/local/bin/jmgr
    # rm /usr/share/man/man8/jmgr.8
    # rm -rf /usr/local/etc/jmgr

