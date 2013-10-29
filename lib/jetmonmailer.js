var config  = require( './config' );

var nodemailer = require('nodemailer');

var smtpTransport = nodemailer.createTransport(
    "SMTP",
    {
        host: config.mailer.HOST,
        port: config.mailer.PORT,
/*        auth: {
            user: config.mailer.USER,
            pass: config.mailer.PASSWORD
        }
*/  }
);

var jetmonMailer = {

    sendStatusChangeMail : function ( server ) {
    	var now = new Date();
		var data = {
			username: server.monitor_url,
			url: server.monitor_url,
			date_and_time: now.toString(),
			downtime: now.getTime() - server.last_status_change,
		}
        var mailOptions = {
            from: config.mailer.FROM,
            to: jetmonMailer.getEmailAddresses( server ),
        };

		if ( server.site_status ) {
			mailOptions.subject = this.template( config.mailer.serverUpSubject, data );
			mailOptions.text = this.template( config.mailer.serverUpHTML, data );
		} else {
			mailOptions.subject = this.template( config.mailer.serverDownSubject, data );
			mailOptions.text = this.template( config.mailer.serverDownHTML, data );
		}
		
        smtpTransport.sendMail( mailOptions, function( error, response ) {
            if ( error ){
                console.log( error );
            } else {
            	if ( config.DEBUG === true )
	                console.log( 'Message sent: ' + response.message );
            }
        });
    },

    sendStillDownMail : function ( server ) {		
		var data = {
			username: server.monitor_url,
			url: server.monitor_url,
			downtime: Math.round( ( config.TIME_BETWEEN_NOTIFICATIONS / 1000 ) % 60 ),
		}

        var mailOptions = {
            from: config.mailer.FROM,
            subject: this.template( config.mailer.serverStillDownSubject, data ), // Subject line
            to: jetmonMailer.getEmailAddresses( server ),
            text: this.template( config.mailer.serverStillDownHTML, data ), // html body
        };

        smtpTransport.sendMail( mailOptions, function( error, response ) {
            if ( error ){
                console.log( error );
            } else {
                console.log( 'Message sent: ' + response.message );
            }
        });
    },

    getEmailAddresses : function ( server ) {
        return server.notify_email_addresses;
    },
    
	exit : function() {
		smtpTransport.close(); // shut down the smtp connection pool
	},

	template : function ( template, data ) {
    	return template.replace( 
    		/%(\w*)%/g, 
    		function( m, key ) {
    			return data.hasOwnProperty( key )? data[key] : "";
    		}
    	);
    },           
};



module.exports = jetmonMailer;
