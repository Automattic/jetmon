verifliers
==========

Overview
--------

The veriflier service checks whether sites are reachable from its location, using the same HEAD request as Jetmon.
This allows the remote deployment of the servers in geographically remote sites, providing a global status of a site.

Building
--------

1) Ensure you have a Qt5 build environment installed. 
2) Run the Qt5 'qmake' executable in the veriflier directory.
3) Run 'make'.

Running
-------

1) Modify the install path if necessary in 'veriflier.sh'.
2) Run './veriflier start|stop|restart|reload'

