
var pool   = require( './dbpools' );

var fromBucketNo = global.config.get( 'BUCKET_NO_MIN' );
var toBucketNo   = global.config.get( 'BUCKET_NO_MIN' ) + global.config.get( 'BATCH_SIZE' );

var arrUpdateStatements = new Array();
var arrUpdateQueue      = new Array();
var conn_write          = new Object();

var database = {
	init : function( success ) {
		if ( ! success ) {
			console.error( "failed to load the DB config file, exiting..." );
			process.exit( 1 );
		}
	},

	handleDisconnect : function() {
		logger.debug( 'attempting to update the db config and reconnect...' );
		pool.config.load( database.init );
	},

	execQuery : function( sqlQuery, callBack ) {
		var poolPrefix = 'USER_';
		if ( -1 !== sqlQuery.indexOf( 'jetpack_' ) )
			poolPrefix = 'MISC_';
		else if ( -1 !== sqlQuery.indexOf( 'languages' ) )
			poolPrefix = 'GLOBAL_';

		// round-robin select the relevant read-only server
		pool.cluster.getConnection( poolPrefix + 'SLAVE*',
								function( err, connection ) {
									if ( err ) {
										logger.debug( 'error connecting to local DC slave db: ' + err );

										// round-robin select a read-only failover server
										pool.cluster.getConnection( poolPrefix + 'FAILOVER*',
																	function( err, connection ) {
																		if ( err ) {
																			callBack( 'error connecting to a remote failover db: ' + err, new Array() );
																		} else {
																			if ( true === global.config.get( 'DEBUG' ) )
																				logger.debug( 'running: ' + sqlQuery );
																			connection.query( sqlQuery, function( error, rows ) {
																										callBack( error, rows );
																										connection.release();
																			});
																		}
										});
									} else {
										if ( true === global.config.get( 'DEBUG' ) )
											logger.debug( 'running: ' + sqlQuery );
										connection.query( sqlQuery, function( error, rows ) {
																	callBack( error, rows );
																	connection.release();
										});
									}
		});
	},

	getNextBatch : function( afterQueryFunction ) {
		var query = 'SELECT * FROM `jetpack_monitor_subscription` WHERE `bucket_no` >= ' +
					fromBucketNo + ' AND `bucket_no` < ' + toBucketNo + ' AND `monitor_active` = 1;';

		database.execQuery( query, function( error, rows ) {
									if ( error ) {
										logger.debug( 'error fetching records: ' + error );
										fromBucketNo -= global.config.get( 'BATCH_SIZE' );
										toBucketNo -= global.config.get( 'BATCH_SIZE' );
									} else {
										afterQueryFunction( rows );
									}
		});

		fromBucketNo = fromBucketNo + global.config.get( 'BATCH_SIZE' );
		if ( fromBucketNo >= global.config.get( 'BUCKET_NO_MAX' ) )
			fromBucketNo = global.config.get( 'BUCKET_NO_MIN' );

		toBucketNo = fromBucketNo + global.config.get( 'BATCH_SIZE' );
		if ( toBucketNo > global.config.get( 'BUCKET_NO_MAX' ) )
			toBucketNo = global.config.get( 'BUCKET_NO_MIN' ) + global.config.get( 'BATCH_SIZE' );

		// if we have 'wrapped' around to the start again, then return that we are finished for this round
		return ( global.config.get( 'BUCKET_NO_MIN' ) === fromBucketNo );
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

	saveNewStatus : function( server ) {
		var query = 'UPDATE `jetpack_monitor_subscription` SET `site_status` = ' + server.site_status;
		if ( SITE_CONFIRMED_DOWN != server.site_status ) {
			query += ",`last_status_change`= '" + database.getNowDateTime(); 
		}
		query += "' WHERE `blog_id` = " + server.blog_id + ';';

		arrUpdateQueue.push( query );

		if ( arrUpdateQueue.length >= global.config.get( 'SQL_UPDATE_BATCH' ) ) {
			if ( arrUpdateStatements.length ) {
				for (var loop = 0; loop < arrUpdateQueue.length; loo++ )
					arrUpdateStatements.push( arrUpdateQueue[ loop ] );
			} else {
				arrUpdateStatements = arrUpdateQueue;
			}
			arrUpdateQueue = [];
			database.commitUpdates();
		}
	},

	commitUpdates : function( callBack ) {
		if ( 0 == arrUpdateStatements.length ) {
			if ( undefined === callBack)
				return;
			else
				callBack();
		}
		pool.cluster.getConnection( 'MISC_MASTER', function( err, conn_write ) {
											if ( undefined != err ) {
												logger.debug( 'error getting a connection: ' + err.code );
												if( undefined === callBack)
													callBack();
											}
											var up_query = '';
											for ( var uploop = 0; uploop < arrUpdateStatements.length; uploop++ )
												up_query = up_query + arrUpdateStatements[uploop];

											if ( true === global.config.get( 'DEBUG' ) )
												logger.debug( "RUNNING batch : " + up_query );

											conn_write.query( up_query, function( error, rows ) {
																		if ( error )
																			logger.debug( 'Error updating: ' + error.code );
																		else
																			arrUpdateStatements = [];

																		conn_write.release();
																		if( undefined !== callBack)
																			callBack();
											});
		});
	},

	getUserIDFromBlogID : function( blogID, callBack ) {

		var query = "SELECT `user_id` FROM `jetpack_tokens_mu` WHERE `blog_id` = " + blogID + " AND `role` = 'administrator';";

		database.execQuery( query, function( error, rows ) {
									var userID = -1
									if ( error || ! rows || ( 0 >= rows.length ) )
										logger.debug( 'error fetching users details: ' + ( ( rows && ( 0 == rows.length ) ) ? 'no results returned' : error ) );
									else
										userID = rows[0].user_id;
									callBack( userID );
		});
	},

	getUserEmail : function( userID, callBack ) {

		var query = "SELECT `user_email` FROM `wp_users` WHERE `ID` = " + userID + ";";

		database.execQuery( query, function( error, rows ) {
									var emailAddr = null;
									if ( error || ! rows || ( 0 >= rows.length ) )
										logger.debug( 'error fetching users details: ' + ( ( rows && ( 0 == rows.length ) ) ? 'no results returned' : error ) );
									else {
										emailAddr = rows[0].user_email;
									}
									callBack( emailAddr );
		});
	},

	getUserLanguage : function(userID, callBack ) {

		query = "SELECT `meta_value` FROM `wp_usermeta` WHERE `user_id` = " + userID + " AND `meta_key` = 'lang_id' LIMIT 1;";

		database.execQuery( query, function( error, rows ) {
									var langID = 1;
									if ( error || ! rows || ( 0 >= rows.length ) )
										logger.debug( 'error fetching users language: ' +
													( ( rows && ( 0 == rows.length ) ) ? 'no results returned' : error ) + '; defaulting to English.');
									else
										langID = rows[0].meta_value;
									callBack( langID );
		});
	},

	getLanguageFromID : function( langID, callBack ) {

		query = "SELECT `slug` FROM `languages` WHERE `lang_id` = " + langID + " LIMIT 1;"

		database.execQuery( query, function( error, rows ) {
									var langCode = 'en';
									if ( error || ! rows || ( 0 >= rows.length ) )
										logger.debug( 'error fetching language from id: ' +
													( ( rows && ( 0 == rows.length ) ) ? 'no results returned' : error ) + '; defaulting to English.');
									else
										langCode = rows[0].slug;
									callBack( langCode );
		});
	}

};

// initialise the database settings
pool.config.load( database.init );

module.exports = database;
