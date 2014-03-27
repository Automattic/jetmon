veriflier service
=================

Overview
--------

The veriflier services check whether sites are reachable from thier location, using the same HEAD request as jetmon. This allows deployment to be done in geographically disparate datacenters, providing a true global status of the site being verified.

Building
--------

1) Ensure you have a Qt5 build environment installed.

2) Run the Qt5 'qmake' executable in the veriflier directory.

3) Run 'make'.

Running
-------

1) Modify the install path if necessary in 'veriflier.sh'.

2) Run './veriflier start|stop|restart|reload'

