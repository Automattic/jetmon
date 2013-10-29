var cluster   = require( 'cluster' );
var config    = require( './config' );

process.title = 'jetmon-worker';

var _watcher = require( '../build/Release/watcher.node' );
var arrCheck = new Array();
var running	 = false;
var pointer  = 0;

var HttpChecker = {
	checkServers: function () {
        try {
            var pointerCurrentMax = pointer + config.NUM_TO_PROCESS;
            if ( pointerCurrentMax > arrCheck.length )
                pointerCurrentMax = arrCheck.length;

            for ( ; pointer < pointerCurrentMax ; pointer++ ) {
                _watcher.http_check( arrCheck[ pointer ].monitor_url, config.HTTP_PORT, pointer, HttpChecker.processResultsCallback );
            }
        }
        catch ( Exception ) {
            console.log( cluster.worker.id + ': ERROR - failed to process the server array: ' + Exception.toString() );
        }
    },

	processResultsCallback: function( serverId, rtt, strDesc ) {
        var server = arrCheck[ serverId ];
        server.rtt = rtt;
        server.processed = true;
        server.checked++;

        if ( server.rtt > 0 && strDesc === "SITE OK" )
        	server.site_status = 1;
        else if ( server.oldStatus != 2 && ( new Date().getTime() ) < ( server.last_status_change + config.TIME_BETWEEN_NOTIFICATIONS ) )
        	server.site_status = 0;
        else
        	server.site_status = 2;

        if ( server.site_status !=  server.oldStatus ) {
            // if site is down and it has not been confirmed
            if ( server.site_status == 0 && server.checked < config.NUM_OF_CHECKS ) {
                server.lastCheck = new Date().getTime();
                process.send( { msgtype: 'recheck', server: server, resp: strDesc } );
            } else if ( server.site_status != 2 ) { // if site is up or has been confirmed down
                process.send( { msgtype: 'notify_status_change', server: server, resp: strDesc } );
            } else {
	     		process.send( { msgtype: 'notify_still_down', server: server, resp: strDesc } );
	     	}
        }

        if ( config.DEBUG === true )
            process.send( { msgtype: 'totals', workerid: cluster.worker.id, server: server } );

        if ( pointer < arrCheck.length ) {
            _watcher.http_check( arrCheck[ pointer ].monitor_url, config.HTTP_PORT, pointer, HttpChecker.processResultsCallback );
            pointer++;
        } else {
            // check if we have any outstanding callbacks
            var still_waiting = false;
            for ( var count in arrCheck ) {
                if ( ! arrCheck[ count ].processed ) {
                    still_waiting = true;
                    break;
                }
            }
            if ( ! still_waiting ) {
                running = false;
                process.send( { msgtype: 'i_need_some_work', workerid: cluster.worker.id } );
            }
        }
    },

	addToQueue: function( arrData ) {
        if ( running ) {
            for ( var count in arrData ) {
                arrCheck.push( arrData[ count ] );
            }
        } else {
            arrCheck = arrData;
            pointer = 0;
            running	 = true;
            setTimeout( HttpChecker.checkServers, 50 );
        }
    },

};

process.on( 'message', function( msg ) {
	try {
		switch (msg.request)
		{
			case 'queue-add': {
				//console.log( cluster.worker.id + ': INFO: received "queue-add" message' );
				HttpChecker.addToQueue( msg.payload );
				break;
			}
			default: {
				console.log( cluster.worker.id + ': INFO: received unknown message "' + msg.request + '"' );
				process.send( { msgtype: 'unknown', workerid: msg.id, payload: 0 } );
				break;
			}
		}
	}
	catch ( Exception ) {
		logger.error( cluster.worker.id + ": ERROR: receiving the Master's message: " + Exception.toString() );
	}
});

setTimeout( function(){ process.send( { msgtype: 'i_need_some_work', workerid: cluster.worker.id } ); }, 1200 );
