
var config     = require( './config' );
var nodemailer = require( 'nodemailer' );
var fs    	   = require( 'fs' );

const SITE_DOWN           = 0;
const SITE_RUNNING        = 1;
const SITE_CONFIRMED_DOWN = 2;

var mailConfig = {
				host: config.mailer.HOST,
				port: config.mailer.PORT,
			};

var smtpTransport = nodemailer.createTransport( 'SMTP', mailConfig );

var mailer = {
	sendStatusChangeMail : function ( server ) {
		if ( undefined == mailer.getEmailAddresses( server ) )
			return;

		if ( true === config.DEBUG )
			logger.debug( 'sendStatusChangeMail, sending to: ' + mailer.getEmailAddresses( server ) );

		var now = new Date();
		var data = {
					username      : server.monitor_url,
					url           : server.monitor_url,
					date_and_time : now.toString(),
					downtime      : now.getTime() - server.last_status_change,
					language	  : mailer.getLanguage( server ),
				};

		var mailOptions = {
					from : config.mailer.FROM,
					to   : mailer.getEmailAddresses( server ),
				};

		if ( SITE_RUNNING == server.site_status ) {
			mailOptions.subject = this.template( 'serverUpSubject', data );
			mailOptions.text = this.template( 'serverUpHTML', data );
		} else {
			mailOptions.subject = this.template( 'serverDownSubject', data );
			mailOptions.text = this.template( 'serverDownHTML', data );
		}

		smtpTransport.sendMail( mailOptions, function( error, response ) {
												if ( error ) {
													logger.debug( error );
												} else {
													if ( true === config.DEBUG )
														logger.debug( 'Message sent: ' + response.message );
												}
		});
	},

	sendStillDownMail : function ( server ) {
		if ( undefined == mailer.getEmailAddresses( server ) )
			return;

		if ( true === config.DEBUG )
			logger.debug( 'sendStillDownMail, sending to: ' + mailer.getEmailAddresses( server ) );

		var data = {
			username : server.monitor_url,
			url      : server.monitor_url,
			downtime : Math.round( ( config.TIME_BETWEEN_NOTIFICATIONS / 1000 ) % 60 ),
			language : mailer.getLanguage( server ),
		}

		var mailOptions = {
			from    : config.mailer.FROM,
			to      : mailer.getEmailAddresses( server ),
			subject : this.template( 'serverStillDownSubject', data ),
			text    : this.template( 'serverStillDownHTML', data ),
		};

		smtpTransport.sendMail( 
			mailOptions,
			function( error, response ) {
				if ( error ) {
					logger.debug( error );
				} else {
					logger.debug( 'Message sent: ' + response.message );
				}
			}
		);
	},

	getEmailAddresses : function ( server ) {
		// This function is going to be replaced with a call to a database function
		return server.notify_email_addresses;
	},

	getLanguage : function ( server ) {
		// This function is going to be replaced with a call to a database function
		return 'en';
	},

	exit : function() {
		smtpTransport.close();
	},

	template : function ( template_name, data ) {
		fs.readFile( 
			config.mailer.TEMPLATES_DIR + template_name + '.' + data.language + '.tpl',
			function( err, template ) {
				if ( ! err && ( undefined != template ) ) {
					return template.replace(
						/%(\w*)%/g, 
						function( m, key ) {
							return data.hasOwnProperty( key ) ? data[key] : '';
						}
					);
				}
				return false;
			}
		);
	},
};

module.exports = mailer;
