
const SECONDS = 1000;
const MINUTES = 60 * SECONDS;
const HOURS   = 60 * MINUTES;
const DAYS    = 24 * HOURS;

var mc       = require( './memcache' );
var db_mysql = require( './database' );

var localUserCache = new Array();
var localBlogCache = new Array();

var user = {
	getUserID : function( blogID, callBack ) {
		userID = user.getLocalBlogCache( blogID );
		if ( userID )
			callBack( userID );
		else
			db_mysql.getUserIDFromBlogID( blogID, function ( userID ) {
													if ( userID )
														user.setLocalBlogCache( blogID, userID );
													callBack( userID );
			});
	},

	getUserObject : function( userID, callBack ) {
		var userDeets = user.getLocalUserCache( userID );

		if ( ! userDeets && mc ) {
			mc.getUserCacheValue( userID, function( userObject ) {
				mc.getUserMetaCacheValue( userID, function( userMeta ) {
					mc.getGlobalCacheValue( userMeta, function( language ) {
										if ( userObject || language ) {
											userDeets = new Object();
											userDeets.user_id = userID;
											if ( userObject )
												userDeets.email_address = userObject.user_email;
											else
												userDeets.email_address = null;
											if ( language )
												userDeets.language = language;
											else
												userDeets.language = null;
										}
										if ( userDeets ) {
											// we have some data, we need to check if both email and language are there, in case we had a partial memcache hit
											if ( null === userDeets.email_address ) {
												db_mysql.getUserEmail( userID, function( userEmail ) {
																				if ( userEmail ) {
																					userDeets.email_address = userEmail;
																					user.setLocalUserCache( userID, userDeets );
																				}
																				callBack( userDeets );
												});
											}
											if ( null === userDeets.language ) {
												db_mysql.getUserLanguage( userID, function( langID ) {
													db_mysql.getLanguageFromID( langID, function( langCode ) {
																				// this always returns something (1=en as default)
																				userDeets.language = langCode;
																				user.setLocalUserCache( userID, userDeets );
																				callBack( userDeets );
													});
												});
											}
										} else {
											db_mysql.getUserEmail( userID, function( userEmail ) {
												db_mysql.getUserLanguage( userID, function( langID ) {
													db_mysql.getLanguageFromID( langID, function( langCode ) {
																			if ( userEmail ) {
																				userDeets = new Object();
																				userDeets.user_id = userID;
																				userDeets.email_address = userEmail;
																				userDeets.language = langCode;
																			}
																			if ( ! userDeets ) {
																				logger.error( 'there were errors retrieving the details for userID: ' + userID );
																			} else {
																				user.setLocalUserCache( userID, userDeets );
																			}
																			callBack( userDeets );
													});
												});
											});
										}
					});
				});
			});
		} else if ( ! userDeets ) {
			db_mysql.getUserEmail( userID, function( userEmail ) {
				db_mysql.getUserLanguage( userID, function( langID ) {
					db_mysql.getLanguageFromID( langID,
												function( langCode ) {
												if ( userEmail ) {
													userDeets = new Object();
													userDeets.user_id = userID;
													userDeets.email_address = userEmail;
													userDeets.language = langCode;
												}
												if ( ! userDeets ) {
													logger.error( 'there were errors retrieving the details for userID: ' + userID );
												} else {
													user.setLocalUserCache( userID, userDeets );
												}
												callBack( userDeets );
					});
				});
			});
		} else {
			// we had a local cache hit
			user.setLocalUserCache( userID, userDeets );
			callBack( userDeets );
		}
	},

	getLocalBlogCache : function( blogID ) {
		for ( var loop = 0; loop < localBlogCache.length; loop++ ) {
			if ( blogID === localBlogCache[ loop ].key )
				return localBlogCache[ loop ].user_id;
		}
		return null;
	},

	setLocalBlogCache : function( blogID, userID ) {
		var cacheObject = new Object();
		cacheObject.expires = new Date().valueOf() +  ( global.config.get( 'LOCAL_CACHE_TTL_MIN' ) * MINUTES );
		cacheObject.key = blogID;
		cacheObject.user_id = userID;
		localBlogCache.push( cacheObject );
	},

	getLocalUserCache : function( iKey ) {
		for ( var loop = 0; loop < localUserCache.length; loop++ ) {
			if ( iKey === localUserCache[ loop ].key )
				return localUserCache[ loop ];
		}
		return null;
	},

	setLocalUserCache : function( iKey, userObject ) {
		userObject.expires = new Date().valueOf() + ( global.config.get( 'LOCAL_CACHE_TTL_MIN' ) * MINUTES );
		userObject.key = iKey;
		localUserCache.push( userObject );
	},

	cleanupCache : function() {
		for ( var loop = localUserCache.length - 1; loop >= 0; loop-- ) {
			if ( new Date().valueOf() > localUserCache[ loop ].expires )
				localUserCache.shift( loop, 1 );
		}
		for ( var loop = localBlogCache.length - 1; loop >= 0; loop-- ) {
			if ( new Date().valueOf() > localBlogCache[ loop ].expires )
				localBlogCache.shift( loop, 1 );
		}
	}
};

// init the mem-cache server settings
mc.loadConfig( function ( success ) {
				if ( ! success ) {
					console.error( 'mem-cache server config failed to load.' );
					mc = null;
				}
});

// cleanup cache periodically
setInterval( user.cleanupCache, global.config.get( 'CACHE_PURGE_TIMER_SEC' ) * SECONDS );

module.exports = user;
