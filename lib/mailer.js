
const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

var nodemailer = require( 'nodemailer' );
var moment 	   = require( 'moment' );
var templates  = require( './templates' );
var user       = require( './user' );

var mailConfig = {
				host: global.config.get( 'mailer' ).HOST,
				port: global.config.get( 'mailer' ).PORT,
			};

var smtpTransport = nodemailer.createTransport( 'SMTP', mailConfig );

var mailer = {
	sendStatusChangeMail : function( server ) {
		user.getUserID( server.blog_id,
						function( userID ) {
							if ( userID <= 0 ) {
								logger.error( 'sendStatusChangeMail, failed to fetch userID for blog_id: ' + server.blog_id );
								return;
							}
							user.getUserObject( userID,
											function( userObject ) {
												if ( undefined === userObject ) {
													logger.error( 'sendStatusChangeMail, failed to fetch user object for userID: ' + userID )
													return;
												}
												if ( true === global.config.get( 'DEBUG' ) )
													logger.debug( 'sendStatusChangeMail, sending to: ' + userObject.email_address );

												var data = mailer.getEmailFieldData( server, userObject.language );

												var mailOptions = {
															from : global.config.get( 'mailer' ).FROM,
															to   : userObject.email_address,
														};

												if ( SITE_RUNNING == server.site_status ) {
													mailOptions.subject = mailer.template( 'serverUpSubject', data );
													mailOptions.text = mailer.template( 'serverUpHTML', data );
												} else {
													mailOptions.subject = mailer.template( 'serverDownSubject', data );
													mailOptions.text = mailer.template( 'serverDownHTML', data );
												}

												smtpTransport.sendMail( mailOptions, function( error, response ) {
																					if ( error ) {
																						logger.error( 'error sending email to ' +
																									userObject.email_address + ' : ' + error.code );
																					} else {
																						if ( true === global.config.get( 'DEBUG' ) )
																							logger.debug( 'Message sent: ' + response.message );
																					}
												});
							});
		});
	},

	sendStillDownMail : function( server ) {
		user.getUserID( server.blog_id,
						function( userID ) {
							if ( userID <= 0 ) {
								logger.error( 'sendStillDownMail, failed to fetch userID for blog_id: ' + server.blog_id );
								return;
							}
							user.getUserObject( userID,
												function( userObject ) {
													if ( undefined === userObject ) {
														logger.error( 'sendStillDownMail, failed to fetch user object for userID: ' + userID )
														return;
													}
													if ( true === global.config.get( 'DEBUG' ) )
														logger.debug( 'sendStillDownMail, sending to: ' + userObject.email_address );

													var data = mailer.getEmailFieldData( server, userObject.language );

													var mailOptions = {
														from    : global.config.get( 'mailer' ).FROM,
														to      : userObject.email_address,
														subject : mailer.template( 'serverStillDownSubject', data ),
														text    : mailer.template( 'serverStillDownHTML', data ),
													};

													smtpTransport.sendMail( mailOptions, function( error, response ) {
																						if ( error ) {
																							logger.error( 'error sending email to ' +
																										userObject.email_address + ' : ' + error.code );
																						} else {
																							logger.debug( 'Message sent: ' + response.message );
																						}
													});
							});
		});
	},

	getEmailFieldData : function ( server, lang ) {
		var now  = moment();
		now.lang( lang );
		var downtime = moment.duration( now.diff( server.last_status_change ) );

		return {
			username      : server.monitor_url,
			url           : server.monitor_url,
			date_and_time : now.format( 'LLLL' ),
			downtime      : downtime.humanize(),
			language	  : lang,
		};
	},

	exit : function() {
		smtpTransport.close();
	},

	template : function( templateKey, data ) {
		return templates.render( data.language  + '_mail.json', templateKey, data )
	}
};

module.exports = mailer;
