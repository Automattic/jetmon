
var config = require( './config' );
var pool   = require( './dbpools' );

var fromBucketNo = config.BUCKET_NO_MIN;
var toBucketNo   = config.BUCKET_NO_MIN + config.BATCH_SIZE;

var arrUpdateStatements = new Array();
var arrUpdateQueue      = new Array();
var conn_write          = new Object();

var database = {
	init : function( success ) {
		if ( ! success ) {
			console.error( "failed to load the DB config file, exiting..." );
			process.exit( 1 );
		}
		// we only have one master, so we keep this connection open and do not do a round-robin select
		pool.cluster.getConnection( 'MASTER', function( err, connection ) {
											if ( undefined != err ) {
												logger.debug( 'error getting a connection: ' + err.code );
												throw err;
												return;
											}
											conn_write = connection;
											conn_write.on( 'error', function( err ) {
												logger.debug( 'database write connection error: ' + err.code );
												if ( 'PROTOCOL_CONNECTION_LOST' === err.code )
													setTimeout( database.handleDisconnect, 2000 );
												else
													throw err;
											});
		});
	},

	handleDisconnect : function() {
		logger.debug( 'attempting to update the db config and reconnect...' );
		pool.config.load( database.init );
	},

	getNextBatch : function( afterQueryFunction ) {
		var query = 'SELECT * FROM jetpack_monitor_subscription WHERE bucket_no >= ' +
					fromBucketNo + ' AND bucket_no < ' + toBucketNo + ' AND  monitor_active = 1 LIMIT 6000;'; //blog_id = 37;' ; //

		if ( true === config.DEBUG )
			logger.debug( query );

		// round-robin select a read-only server
		pool.cluster.getConnection( 'SLAVE*',
								function( err, connection ) {
									if ( err ) {
										logger.debug( 'error connecting to local DC slave db: ' + err.code );
										// round-robin select a read-only failover server
										pool.cluster.getConnection( 'FAILOVER*',
																	function( err, connection ) {
																		if ( err ) {
																			logger.debug( 'error connecting to a remote failover db: ' + err.code );
																			fromBucketNo -= config.BATCH_SIZE;
																			toBucketNo -= config.BATCH_SIZE;
																		} else {
																			connection.query( query, function( error, rows ) {
																									if ( error ) {
																										logger.debug( error.code );
																										throw error;
																									}
																									connection.release();
																									afterQueryFunction( rows );
																			});
																		}
																	});
									} else {
										connection.query( query, function( error, rows ) {
																if ( error ) {
																	logger.debug( 'error fetching records: ' + error.code );
																	fromBucketNo -= config.BATCH_SIZE;
																	toBucketNo -= config.BATCH_SIZE;
																} else {
																	connection.release();
																	afterQueryFunction( rows );
																}
										});
									}
		});

		fromBucketNo = fromBucketNo + config.BATCH_SIZE;
		if ( fromBucketNo >= config.BUCKET_NO_MAX )
			fromBucketNo = config.BUCKET_NO_MIN;

		toBucketNo = fromBucketNo + config.BATCH_SIZE;
		if ( toBucketNo > config.BUCKET_NO_MAX )
			toBucketNo = config.BUCKET_NO_MIN + config.BATCH_SIZE;

		// if we have 'wrapped' around to the start again, then return that we are finished for this round
		return ( config.BUCKET_NO_MIN === fromBucketNo );
	},

	getNowDateTime : function() {
		var now = new Date();
		return now.getFullYear() + "-" +
				( 1 == ( 1 + now.getMonth() ).length ? '0' + ( 1 + now.getMonth() ) : ( 1 + now.getMonth() ) ) + "-" +
				( 1 == now.getDate().toString().length    ? '0' + now.getDate()    : now.getDate() )  + " " +
				( 1 == now.getHours().toString().length   ? '0' + now.getHours()   : now.getHours() ) + ":" +
				( 1 == now.getMinutes().toString().length ? '0' + now.getMinutes() : now.getMinutes() ) + ":" +
				( 1 == now.getSeconds().toString().length ? '0' + now.getSeconds() : now.getSeconds() );
	},

	saveNewStatus : function ( server ) {
		var query = 'UPDATE jetpack_monitor_subscription SET site_status=' + server.site_status +
					",last_status_change='" + database.getNowDateTime() + "' WHERE blog_id=" + server.blog_id + ';';

		arrUpdateQueue.push( query );

		if ( arrUpdateQueue.length >= config.SQL_UPDATE_BATCH ) {
			arrUpdateStatements = arrUpdateQueue;
			arrUpdateQueue = [];
			database.commitUpdates();
		}
	},

	commitUpdates : function() {
		if ( 0 == arrUpdateStatements.length )
			return;
		var up_query = '';
		for ( var uploop = 0; uploop < arrUpdateStatements.length; uploop++ )
			up_query = up_query + arrUpdateStatements[uploop];

		//if ( true === config.DEBUG )
		//	logger.debug( "RUNNING batch : " + up_query );

		conn_write.query( up_query, function( error, rows ) {
									if ( error ) {
										logger.debug( 'Error updating: ' + error.code );
										throw error;
									}
		});
	}
};

pool.config.load( database.init );

module.exports = database;
