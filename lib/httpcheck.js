
process.title = 'jetmon-worker';

const PING_CHECK      = "1";
const PING_NUM_CHECKS = 5;
const PING_NUM_PASS   = 4;

const HTTP_CHECK      = "2";
const HTTP_PORT       = 80;

const NUM_OF_CHECKS = 4;
const TIME_BETWEEN_CHECKS = 20; //seconds

const NUM_TO_PROCESS  = 10;

var _watcher = require( '../build/Release/watcher.node' );
var arrCheck = new Array();
var running	 = false;
var pointer  = 0;

var http_checker = {
	check_servers: function () {
        try {
            running = true;
            pointer_current_max = pointer + NUM_TO_PROCESS;
            if ( pointer_current_max > arrCheck.length )
                pointer_current_max = arrCheck.length;

            for ( ; pointer < pointer_current_max ; pointer++ ) {
                _watcher.http_check( arrCheck[ pointer ].url, HTTP_PORT, pointer, http_checker.process_results_callback );
            }
        }
        catch ( Exception ) {
            console.log( process.pid + ': ERROR - failed to process the server array: ' + Exception.toString() );
        }
    },

	process_results_callback: function( server_idx, rtt, str_desc ) {
        var server = arrCheck[ server_idx ];
        server.rtt = rtt;
        server.processed++;
        server.last_check = process.hrtime()[0];

        var new_status = ~~( server.rtt > 0 );

        if ( server.status !=  new_status ) {
            // if site is down
            if ( ! new_status || server.processed < NUM_OF_CHECKS ) {
                // check again in TIME_BETWEEN_CHECKS milliseconds
                arrCheck.push( server );
            } else {
                http_checker.status_changed( server_idx, str_desc );
                server.processed = NUM_OF_CHECKS;
            }
        } else {
            server.processed = NUM_OF_CHECKS;
        }

        server.status = new_status;

        process.send( { msgtype: 'totals', workerid: process.pid, server: server } );

        if ( pointer < arrCheck.length ) {
            while( process.hrtime()[0] - arrCheck[ pointer ].last_check < TIME_BETWEEN_CHECKS );
            _watcher.http_check( arrCheck[ pointer ].url, HTTP_PORT, pointer, http_checker.process_results_callback );
            pointer++;
        } else {
            // check if we have any outstanding callbacks
            var still_waiting = false;
            for ( var count in arrCheck ) {
                if ( arrCheck[ count ].processed < NUM_OF_CHECKS ) {
                    still_waiting = true;
                    break;
                }
            }
            if ( ! still_waiting ) {
                // if not waiting for any callback, then we exit for this test code,
                // normally we would just clean up the array and wait for more data
                process.exit( 0 );

                // this will be used when adding new items to the queue, currently never reached
                running = false;
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
            setTimeout( http_checker.check_servers, 50 );
        }
    },

    status_changed: function( server_idx, str_desc ) {
        process.send(  { msgtype: 'notify', server: arrCheck[ server_idx ], resp: str_desc } );
    },
};

process.on( 'message', function( msg ) {
	try {
		switch (msg.request)
		{
			case 'queue-add': {
				//console.log( process.pid + ': INFO: received "queue-add" message' );
				http_checker.add_to_queue( msg.payload );
				break;
			}
			default: {
				console.log( process.pid + ': INFO: received unknown message "' + msg.request + '"' );
				process.send( { msgtype: 'unknown', workerid: msg.id, payload: 0 } );
				break;
			}
		}
	}
	catch ( Exception ) {
		logger.error( process.pid + ": ERROR: receiving the Master's message: " + Exception.toString() );
	}
});

