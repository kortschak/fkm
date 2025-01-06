# `fkm`

The `fkm` (fetch keymapp metadata) program allows the ZSA `keymapp` program to be used in places where network access by unauditable software is not allowed.

`keymapp` requires network access to collect metadata for the keyboards it is managing. Since it is closed source, we cannot verify that this is the only thing it is doing. So `fkm` allows constructing the necessary file for `keymapp` while using an application firewall to block network access by `keymapp`. `fkm` can be audited and is a simple program.