
process.title = 'jetmon.js';

var cluster 		= require( 'cluster' );

var g_count_success = 0;
var g_count_error 	= 0;
var start_time		= new Date().getTime();

var sites_count		= 0;
var lastLoop 		= new Date().getTime();

var arrWorkers 		= new Array();
var numWorkers 		= 200;				// Number of Workers to create

var mysql           = require('mysql');
var pool            = mysql.createPool({
    host     : 'localhost',
    user     : '',
    password : '',
    database : 'jetmon',
});

var s_file_name 	= "urls.csv";

console.log( 'booting jetmon.js' );

process.on( 'SIGINT', print_totals_exit );
process.on( 'EXIT',   print_totals_exit );

process.on( 'uncaughtException', function( err_desc ) {
	console.log( 'uncaughtException error: ' + err_desc );
});

cluster.setupMaster( {
	exec   : './lib/httpcheck.js',
	silent : false,
});

cluster.on( 'exit', function( worker, code, signal ) {
	for ( var count in arrWorkers ) {
		if ( worker.process.pid == arrWorkers[ count ].worker.process.pid ) {
			arrWorkers.splice( count, 1 );
			break;
		}
	}
});

function print_totals_exit() {
	console.log( '' );
	console.log( 'Error: ' + g_count_error );
	console.log( 'Success: ' + g_count_success );
	console.log( 'Total: ' + (g_count_success + g_count_error) );

	var now = new Date().getTime();
	console.log( 'Time: ' + Math.floor( (now - start_time) / 60000 ) + 'm '
						+ ( ( (now - start_time) % 60000 ) / 1000 ) + 's');
	process.exit( 0 );
}

function worker_msg_callback( msg ) {
    try {
		switch ( msg.msgtype ) {
            case "totals":
                if ( msg.status )
                    g_count_success ++
                else
                    g_count_error ++
                sites_count++;
                break;
            case "notify":
                pool.getConnection(function(err, connection) {
                    connection.query( 'UPDATE main_table SET status = ' + msg.server.status + ', last_status_change_time = NOW() WHERE blog_id=' + msg.server.blog_id, function(err, rows) {
                        connection.release();
                    });
                });
                console.log( msg.server.url + ' rtt: ' + msg.server.rtt  + '  new status: ' + msg.server.status + '  ' +  msg.server.blog_id );
                break;
			default:
		}
	}
	catch ( Exception ) {
		console.log( "Error receiving worker's message: " + Exception.toString() );
	}
}

function update_sites_per_sec() {
	console.log( 'sites/sec = ' + sites_count + ' - ' + arrWorkers.length + '/' + numWorkers + ' working' );
	sites_count = 0;
    lastLoop = new Date().getTime();
    if ( 0 == arrWorkers.length )
    	print_totals_exit();
    else
    	setTimeout( update_sites_per_sec, 1000 );
}

// This routine would be replaced by a database call to MySQL
function send_workers_queues() {
	var fs = require('fs');

    pool.getConnection(function(err, connection) {
        connection.query( 'SELECT * FROM main_table', function(err, rows) {
            // And done with the connection.
            connection.release();
            var arrObjects = new Array();
            for ( var i = 0; i < rows.length; i++ ) {
                var server = rows[i];
                server.processed = 0;
                server.confirmed = false;
                server.rtt = 0
                arrObjects.push( server );
            }
            var bucket_size = Math.ceil( ( arrObjects.length ) / numWorkers);

            console.log( "Total servers: " + arrObjects.length );
            console.log( "bucket_size: " + bucket_size );

            start_time = new Date();
            lastLoop = new Date().getTime();
            setTimeout( update_sites_per_sec, 1000 );

            for (var i = 0; i < arrWorkers.length; i++) {
                var pointer_max = ( arrWorkers[i].worker.id * bucket_size );
                var pointer = ( pointer_max - bucket_size );

                if ( pointer < 0 )
                    pointer = 0;
                if ( pointer_max > arrObjects.length )
                    pointer_max = arrObjects.length;

                arrWorkers[i].worker.send( { id: arrWorkers[i].worker.id, request: 'queue-add', payload: arrObjects.slice( pointer, pointer_max ) } );
            }
        });
    });
}

// Create all our workers and keep an array of them to allow communication back and forth
for (var i = 0; i < numWorkers; i++) {
	var worker = cluster.fork();
	worker.on( 'message', worker_msg_callback );

	var oWorker = new Object();
	oWorker.worker = worker;
	oWorker.site_queue = new Array();

	arrWorkers.push( oWorker );
}

// Starts the main processing in 200ms
setTimeout( send_workers_queues, 200 );

