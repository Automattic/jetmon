
process.title = 'jetmon-worker';

const PING_CHECK      = "1";
const PING_NUM_CHECKS = 5;
const PING_NUM_PASS   = 4;

const HTTP_CHECK      = "2";
const HTTP_PORT       = 80;

const NUM_OF_CHECKS = 4;
const TIME_BETWEEN_CHECKS = 20000; //milliseconds

const NUM_TO_PROCESS  = 10;

var _watcher = require( '../build/Release/watcher.node' );
var arrCheck 		= new Array();
var running			= false;
var pointer			= 0;

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
        arrCheck[ server_idx ].rtt = rtt;
        arrCheck[ server_idx ].processed++;

        var new_status = ~~( arrCheck[ server_idx ].rtt > 0 );

        if ( arrCheck[ server_idx ].status !=  new_status ) {
            if ( arrCheck[ server_idx ].processed < NUM_OF_CHECKS ) {
                // check again in TIME_BETWEEN_CHECKS milliseconds
                setTimeout( function(){_watcher.http_check( arrCheck[ server_idx ].url, HTTP_PORT, server_idx, http_checker.process_results_callback );}, TIME_BETWEEN_CHECKS );
                return;
            }
            arrCheck[ server_idx ].status = new_status;
            http_checker.status_changed( server_idx, str_desc );
        }
        arrCheck[ server_idx ].processed = NUM_OF_CHECKS;
        /*
         if ( arrCheck[ server_idx ].rtt > 0 ) {
         process.send(  { msgtype: 'totals', workerid: process.pid, success: 1, error: 0 } );
         //if ( ( "200" != str_desc ) && ( "302" != str_desc ) )
         //	console.log( process.pid + ': ' + str_desc + ' ' + arrCheck[ server_idx ].url );
         } else {
         process.send(  { msgtype: 'totals', workerid: process.pid, success: 0, error: 1 } );
         //console.log( process.pid + ': error desc = ' + str_desc );
         }
         */
        process.send(  { msgtype: 'totals', workerid: process.pid, status: new_status } );

        if ( pointer < arrCheck.length ) {
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

