#!/bin/bash

### BEGIN INIT INFO
# Provides:          veriflier
# Required-Start:
# Required-Stop:
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: Start/stop veriflier
# Description:       Start/stop the service.
### END INIT INFO

# installation directory
INSTALL_DIR=/opt/veriflier

function startservice {
	if [ ! -d ${INSTALL_DIR} ]; then
		echo "the jetmon veriflier is not installed in the correct directory: ${INSTALL_DIR}"
		exit 1
	fi
	if [ -f /var/run/veriflier.pid ]; then
		pid="`cat /var/run/veriflier.pid`"
		if [ -z "$pid" ]; then
			pid=0
		fi
		if [ $pid -gt 0 ] ; then
			echo "veriflier service is already running"
			exit 1
		fi
	fi
	# create the required log directory
	if [ ! -d "${INSTALL_DIR}/logs" ]; then
		mkdir "${INSTALL_DIR}/logs"
	fi

	echo "Starting veriflier"
	(
		cd "${INSTALL_DIR}"
		( ${INSTALL_DIR}/veriflier >/dev/null 2>&1 )&
		echo $! > /var/run/veriflier.pid
	)
}

function stopservice {
	if [ -f /var/run/veriflier.pid ]; then
		pid="`cat /var/run/veriflier.pid`"
		if [ -z "$pid" ]; then
			echo "There was an error loading the pid file."
		else
			echo "Stopping veriflier with pid $pid"
			kill -15 $pid
		fi
		rm -f /var/run/veriflier.pid
	else
		echo "There is no veriflier process running."
	fi
}

function reload_config {
	pid="`ps -ef | grep 'veriflier' | grep -v 'grep' | awk ' { print $(2) }'`"
	if [ -z "$pid" ]; then
		pid=0
	fi
	if [ $pid -gt 0 ]; then
		echo "Reloading veriflier config"
		kill -SIGHUP $pid
	else
		echo "There is no veriflier process running."
	fi
}

case "$1" in
start )
	startservice
	;;
stop )
	stopservice
	;;
restart )
	stopservice
	sleep 1
	startservice
	;;
reload )
	reload_config
	;;
* )
	echo "Usage:$0 start|stop|restart|reload"
	exit 1
	;;
esac
exit 0
