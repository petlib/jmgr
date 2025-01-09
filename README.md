# jmgr
yet another jail management tool.

- jmgr is yet another FreeBSD jail management tool wich uses userland commands for basic administration of jails.

- jmgr can create/clone jails as 'thick jails on ZFS' or just 'thick jails' on a ordinary filesystem and stores the jail configuration in /etc/jail.conf.d.

- jmgr is configurable, use command: jmgr config to see actual configuration. Settings can be adjusted in the
jmgr system wide config file jmgr.conf in the /usr/local/etc/jmgr directory.

- FreeBSD Jails on ZFS is a powerful feature. Altough for the casual user(me) jail administration can be complicated. This is an attempt to simplify some of the tasks involved in create,run,backup,update,upgrade,rollback and destroy ordinary jails. jmgr 
does not do anything particulary fancy, it just uses the functions avaliable with jail(8), jail.conf(8) and zfs(8)

- To give it a try, follow the instructions in 'Install.txt'.