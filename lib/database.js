
const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

const SECONDS = 1000;
const MINUTES = 60 * SECONDS;
const HOURS   = 60 * MINUTES;
const DAYS    = 24 * HOURS;

var pool = require( './dbpools' );

var fromBucketNo = global.config.get( 'BUCKET_NO_MIN' );
var toBucketNo   = global.config.get( 'BUCKET_NO_MIN' ) + global.config.get( 'BATCH_SIZE' );

var arrUpdateStatements = [];
var arrUpdateQueue      = [];
var reloadConfig        = false;

var database = {
	init : function( success ) {
		if ( ! success ) {
			console.error( 'failed to load the DB config file, exiting...' );
			process.exit( 1 );
		}
	},

	updateConfig : function() {
		logger.debug( 'checking if the DB config file has been updated.' );
		pool.config.update(
			function( updated ) {
				if ( updated ) {
					logger.debug( 'updated DB config file detected, setting reloadConfig variable.' );
					reloadConfig = true;
				} else {
					logger.debug( 'no DB config update detected.' );
				}
			});
	},

	execQuery : function( sqlQuery, callBack ) {
		if ( reloadConfig ) {
			pool.config.reload();
			reloadConfig = false;
		}
		var poolPrefix = new String( 'USER_' );
		if ( -1 !== sqlQuery.indexOf( 'jetpack_' ) ) {
			poolPrefix = 'MISC_';
		} else if ( -1 !== sqlQuery.indexOf( 'languages' ) ) {
			poolPrefix = 'GLOBAL_';
		}
		// round-robin select the relevant read-only server
		pool.cluster.getConnection(
			poolPrefix + 'SLAVE*',
			function( err, connection ) {
				if ( err ) {
					logger.error( 'error connecting to local DC slave db: ' + err );

					// round-robin select a read-only failover server
					pool.cluster.getConnection(
						poolPrefix + 'FAILOVER*',
						function( err, connection ) {
							if ( err ) {
								callBack( 'error connecting to a remote failover db: ' + err, new Array() );
							} else {
								if ( true === global.config.get( 'DEBUG' ) ) {
									logger.debug( 'running: ' + sqlQuery );
								}
								connection.query(
									sqlQuery,
									function( error, rows ) {
										callBack( error, rows );
										connection.release();
									});
							}
					});
				} else {
					if ( true === global.config.get( 'DEBUG' ) )
						logger.debug( 'running: ' + sqlQuery );
					connection.query(
						sqlQuery,
						function( error, rows ) {
							callBack( error, rows );
							connection.release();
						});
				}
			});
	},

	getNextBatch : function( afterQueryFunction ) {
		var query = 'SELECT `blog_id`, `monitor_url`, `site_status`, `last_status_change` ' +
			'FROM `jetpack_monitor_sites` WHERE `bucket_no` >= ' +
			fromBucketNo + ' AND `bucket_no` < ' + toBucketNo + ' AND `monitor_active` = 1;';

		logger.log(query);
		database.execQuery(
			query,
			function( error, rows ) {
				if ( error ) {
					logger.debug( 'error fetching records: ' + error );
					fromBucketNo -= global.config.get( 'BATCH_SIZE' );
					toBucketNo -= global.config.get( 'BATCH_SIZE' );
				} else {
					afterQueryFunction( rows );
				}
			});

		fromBucketNo = fromBucketNo + global.config.get( 'BATCH_SIZE' );
		if ( fromBucketNo >= global.config.get( 'BUCKET_NO_MAX' ) ) {
			fromBucketNo = global.config.get( 'BUCKET_NO_MIN' );
		}
		toBucketNo = fromBucketNo + global.config.get( 'BATCH_SIZE' );
		if ( toBucketNo > global.config.get( 'BUCKET_NO_MAX' ) ) {
			toBucketNo = global.config.get( 'BUCKET_NO_MIN' ) + global.config.get( 'BATCH_SIZE' );
		}
		// if we have 'wrapped' around to the start again, then return that we are finished for this round
		return ( global.config.get( 'BUCKET_NO_MIN' ) === fromBucketNo );
	},

	getNowDateTime : function() {
		var now = new Date();
		return now.getFullYear() + '-' +
			( 1 == ( 1 + now.getMonth() ).length ? '0' + ( 1 + now.getMonth() ) : ( 1 + now.getMonth() ) ) + '-' +
			( 1 == now.getDate().toString().length    ? '0' + now.getDate()    : now.getDate() )  + ' ' +
			( 1 == now.getHours().toString().length   ? '0' + now.getHours()   : now.getHours() ) + ':' +
			( 1 == now.getMinutes().toString().length ? '0' + now.getMinutes() : now.getMinutes() ) + ':' +
			( 1 == now.getSeconds().toString().length ? '0' + now.getSeconds() : now.getSeconds() );
	},

	commitUpdates : function( callBack ) {
		if ( 0 == arrUpdateStatements.length ) {
			if ( undefined !== callBack )
				callBack();
			return;
		}
		if ( reloadConfig ) {
			pool.config.reload();
			reloadConfig = false;
		}
		pool.cluster.getConnection(
			'MISC_MASTER',
			function( err, conn_write ) {
				if ( undefined != err ) {
					logger.error( 'error getting a connection: ' + err.code );
					if ( undefined !== callBack ) {
						callBack();
					}
					return;
				}
				var up_query = '';
				for ( var uploop = 0; uploop < arrUpdateStatements.length; uploop++ ) {
					up_query = up_query + arrUpdateStatements[uploop];
				}
				if ( true === global.config.get( 'DEBUG' ) ) {
					logger.debug( 'RUNNING batch : ' + up_query );
				}
				conn_write.query(
					up_query,
					function( error, rows ) {
						if ( error ) {
							logger.error( 'Error updating: ' + error.code );
						} else {
							arrUpdateStatements = [];
						}
						conn_write.release();
						if ( undefined !== callBack ) {
							callBack();
						}
					});
			});
	}
};

// initialise the database settings
pool.config.load( database.init );

// set a repeating 'tick' to perform the database config update checks and conditional reloads
setInterval( database.updateConfig, global.config.get( 'DB_CONFIG_UPDATES_MIN' ) * MINUTES );

module.exports = database;

