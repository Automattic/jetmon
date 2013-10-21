var cluster   = require( 'cluster' );
var config    = require( './config' );

process.title = 'jetmon-worker';

var _watcher = require( '../build/Release/watcher.node' );
var arrCheck = new Array();
var running	 = false;
var pointer  = 0;

var http_checker = {
	check_servers: function () {
        try {
            pointer_current_max = pointer + config.NUM_TO_PROCESS;
            if ( pointer_current_max > arrCheck.length )
                pointer_current_max = arrCheck.length;

            for ( ; pointer < pointer_current_max ; pointer++ ) {
                _watcher.http_check( arrCheck[ pointer ].monitor_url, config.HTTP_PORT, pointer, http_checker.process_results_callback );
            }
        }
        catch ( Exception ) {
            console.log( cluster.worker.id + ': ERROR - failed to process the server array: ' + Exception.toString() );
        }
    },

	process_results_callback: function( server_idx, rtt, str_desc ) {
        var server = arrCheck[ server_idx ];
        server.rtt = rtt;
        server.processed = true;
        server.checked++;
        server.last_check = process.hrtime()[0];

        var old_status = server.site_status;

        server.site_status =  ~~( server.rtt > 0 );

        if (  ( server.checked > 1 && ! server.site_status ) || ( server.site_status !=  old_status ) ) {
            // if site is down and it has not been confirmed
            if ( ! server.site_status && server.checked < config.NUM_OF_CHECKS ) {
                process.send(  { msgtype: 'recheck', server: server, resp: str_desc } );
            } else { // if site is up or has been confirmed down
                http_checker.status_changed( server, str_desc );
            }
        }

        process.send( { msgtype: 'totals', workerid: cluster.worker.id, server: server } );

        if ( pointer < arrCheck.length ) {
            _watcher.http_check( arrCheck[ pointer ].monitor_url, config.HTTP_PORT, pointer, http_checker.process_results_callback );
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

	add_to_queue: function( arrData ) {
        if ( running ) {
            for ( var count in arrData ) {
                arrCheck.push( arrData[ count ] );
            }
        } else {
            arrCheck = arrData;
            pointer = 0;
            running	 = true;
            setTimeout( http_checker.check_servers, 50 );
        }
    },

    status_changed: function( server, str_desc ) {
        process.send(  { msgtype: 'notify', server: server, resp: str_desc } );
    },
};

process.on( 'message', function( msg ) {
	try {
		switch (msg.request)
		{
			case 'queue-add': {
				//console.log( cluster.worker.id + ': INFO: received "queue-add" message' );
				http_checker.add_to_queue( msg.payload );
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
