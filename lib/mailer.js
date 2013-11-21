
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
		var templateKey = ( SITE_RUNNING == server.site_status ) ? 'serverUp' : 'serverDown';
		this.sendMail( server, templateKey );
	},

	sendStillDownMail : function( server ) {
		this.sendMail( server, 'serverStillDown' );
	},

	sendMail : function ( server, templateKey ) {
		user.getUserID( server.blog_id,
				function( userID ) {
					if ( userID <= 0 ) {
						logger.error( 'sendStillDownMail, failed to fetch userID for blog_id: ' + server.blog_id );
						return;
					}
					user.getUserObject( userID,
										function( userObject ) {
											if ( undefined === userObject ) {
												logger.error( templateKey + ' Mail, failed to fetch user object for userID: ' + userID )
												return;
											}
											if ( true === global.config.get( 'DEBUG' ) )
												logger.debug( templateKey + ' Mail, sending to: ' + userObject.email_address );

											var mailOptions = mailer.getEmailData( server, userObject, templateKey );

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

	getEmailData : function ( server, userObject, templateKey ) {
		var data = this.getEmailFieldData( server, userObject );
		return {
			from    : global.config.get( 'mailer' ).FROM,
			to      : userObject.email_address,
			subject : this.template( templateKey + 'Subject', data ),
			text    : this.template( templateKey + 'Text', data ),
			html    : this.getHTML( templateKey, data ),
		};
	},

	getEmailFieldData : function ( server, userObject ) {
		var now  = moment();
		now.lang( userObject.language );
		var downtime = moment.duration( now.diff( server.last_status_change ) );
		var host_url = server.monitor_url;
		if ( -1 == host_url.indexOf( 'http://' ) )
			host_url = 'http://' + host_url;

		return {
			username      : userObject.first_name,
			url           : host_url,
			admin_url     : host_url + '/wp-admin/admin.php?page=jetpack#monitor',
			date_and_time : now.format( 'LLLL' ),
			downtime      : downtime.humanize(),
			language	  : userObject.language,
		};
	},

	exit : function() {
		smtpTransport.close();
	},

	getHTML: function( templateKey, data ) {
		return templates.renderHTML( data.language  + '_mail.json', 'base.html', templateKey, data );
	},

	template : function( templateKey, data ) {
		return templates.render( data.language  + '_mail.json', templateKey, data );
	},

};

module.exports = mailer;
