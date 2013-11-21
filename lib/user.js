
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
											if ( userObject && ( -1 !== userObject.indexOf( "user_email" ) ) ) {
												var s_email = userObject.substr( userObject.indexOf( "user_email" ) + 11 );
												s_email = s_email.substr( s_email.indexOf( '"' ) + 1 );
												s_email = s_email.substr( 0, s_email.indexOf( '"' ) );
												userDeets.email_address = s_email;
											} else {
												userDeets.email_address = null;
											}
											if ( userObject && ( -1 !== userObject.indexOf( "first_name" ) ) ) {
												var s_name = userObject.substr( userObject.indexOf( "first_name" ) + 11 );
												s_name = s_name.substr( s_name.indexOf( '"' ) + 1 );
												s_name = s_name.substr( 0, s_name.indexOf( '"' ) );
												userDeets.first_name = s_name;
											} else {
												userDeets.first_name = null;
											}
											if ( language )
												userDeets.language = language;
											else
												userDeets.language = null;
										}
										if ( userDeets ) {
											// we have some data, but we need to check if we have all, in case we had a partial memcache hit
											if ( ( null === userDeets.email_address ) || ( null === userDeets.first_name ) ) {
												db_mysql.getUserName( userID, function ( userName ) {
													db_mysql.getUserEmail( userID, function( userEmail ) {
																					// only no email is a show stopper, if we don't have the first name,
																					// we just generalise the email content
																					if ( userEmail ) {
																						userDeets.first_name = userName;
																						userDeets.email_address = userEmail;
																						user.setLocalUserCache( userID, userDeets );
																					}
																					callBack( userDeets );
													});
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
											this.getUserDetailsFromDB ( userID, function( userDeets ) {
																			if ( userDeets )
																				user.setLocalUserCache( userID, userDeets );
																			callBack( userDeets );
											});
										}
					});
				});
			});
		} else if ( ! userDeets ) {
			this.getUserDetailsFromDB ( userID, function( userDeets ) {
													if ( userDeets )
														user.setLocalUserCache( userID, userDeets );
													callBack( userDeets );
			});
		} else {
			// we had a local cache hit
			callBack( userDeets );
		}
	},

	getUserDetailsFromDB : function( userID, callBack ) {
		db_mysql.getUserName( userID, function ( userName ) {
			db_mysql.getUserEmail( userID, function( userEmail ) {
									// only no email is a show stopper, if we don't have the first name, we just generalise the email content
									if ( ! userEmail ) {
										logger.error( 'there were errors retrieving the user email from mysql for userID: ' + userID );
										callBack( false );
										return;
									}
									userDeets = new Object();
									userDeets.user_id = userID;
									userDeets.first_name = userName;
									userDeets.email_address = userEmail;

									db_mysql.getUserLanguage( userID, function( langID ) {
																	if ( ! langID ) {
																		logger.error( 'there were errors retrieving the language from mysql for userID: ' + userID );
																		callBack( false );
																		return;
																	}
																	db_mysql.getLanguageFromID( langID, function( langCode ) {
																										// this always returns something (1=en as default)
																										userDeets.language = langCode;
																										callBack( userDeets );
																	});
									});
			});
		});
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
		for ( var loop = 0; loop < localBlogCache.length; loop++ ) {
			if ( blogID === localBlogCache[ loop ].key ) {
				localBlogCache[ loop ] = cacheObject;
				return;
			}
		}
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
		for ( var loop = 0; loop < localUserCache.length; loop++ ) {
			if ( iKey === localUserCache[ loop ].key ) {
				localUserCache[ loop ] = userObject;
				return;
			}
		}
		localUserCache.push( userObject );
	},

	cleanupCache : function() {
		for ( var loop = localUserCache.length - 1; loop >= 0; loop-- ) {
			if ( new Date().valueOf() > localUserCache[ loop ].expires )
				localUserCache.splice( loop, 1 );
		}
		for ( var loop = localBlogCache.length - 1; loop >= 0; loop-- ) {
			if ( new Date().valueOf() > localBlogCache[ loop ].expires )
				localBlogCache.splice( loop, 1 );
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
setInterval( user.cleanupCache, global.config.get( 'LOCAL_CACHE_PURGE_SEC' ) * SECONDS );

module.exports = user;
