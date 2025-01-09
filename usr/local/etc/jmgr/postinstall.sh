#!/bin/sh

# jmgr(8) post installer
#
# Author: peter@libassi.se
#
# Just install bare minimum.

# 
if [ "$#" -ne 3 ]
  then
    echo "Need jail name and jail dir and config file"
    exit 1
fi
JAIL=$1
JAILDIR=$2
JCONFIG=$3

#	Copy timezone and resolv.conf to the jails /etc
install -m 0444 /etc/localtime ${JAILDIR}/etc
install -m 0644 /etc/resolv.conf ${JAILDIR}/etc
