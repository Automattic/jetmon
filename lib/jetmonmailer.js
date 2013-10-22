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

    sendMail : function ( server ) {

        var mailOptions = {
            from: config.mailer.FROM,
            subject: jetmonMailer.getSubject( server ), // Subject line
            to: jetmonMailer.getEmailAddresses( server ),
            text: jetmonMailer.getEmailText( server ), // plaintext body
            html: jetmonMailer.getEmailHtml( server ) // html body
        };

        smtpTransport.sendMail( mailOptions, function( error, response ) {
            if ( error ){
                console.log( error );
            } else {
                console.log( 'Message sent: ' + response.message );
            }
        });
    },

    getSubject : function ( server ) {
        var txt = server.site_status?'Site Up':'Site Down';
        return 'SUBJECT: ' + txt;
    },

    getEmailAddresses : function ( server ) {
        return server.notify_email_addresses;
    },

    getEmailText : function ( server ) {
        var txt = server.site_status?'Site Up':'Site Down';
        return 'PLAIN TEXT: ' + txt;
    },

    getEmailHtml : function ( server ) {
        var txt = server.site_status?'Site Up':'Site Down';
        return 'HTML EMAIL: ' + txt;
    },
    
	exit : function() {
		smtpTransport.close(); // shut down the smtp connection pool
	}            
};

module.exports = jetmonMailer;
