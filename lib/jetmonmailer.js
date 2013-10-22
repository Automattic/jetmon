var config  = require( './config' );

var nodemailer = require('nodemailer');

var smtpTransport = nodemailer.createTransport(
    "SMTP",
    {
        host: config.mailer.host,
        port: config.mailer.port,
        auth: {
            user: config.mailer.user,
            pass: config.mailer.password
        }
    }
);

var jetmonmailer = {

    sendMail : function ( server ) {

        var mailOptions = {
            from: config.mailer.FROM,
            subject: jetmonmailer.getSubject( server ), // Subject line
            to: jetmonmailer.getEmailAddresses( server ),
            text: jetmonmailer.getEmailText( server ), // plaintext body
            html: jetmonmailer.getEmailHtml( server ) // html body
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
        return server.email_addresses;
    },

    getEmailText : function ( server ) {
        var txt = server.site_status?'Site Up':'Site Down';
        return 'PLAIN TEXT: ' + txt;
    },

    getEmailHtml : function ( server ) {
        var txt = server.site_status?'Site Up':'Site Down';
        return 'HTML EMAIL: ' + txt;
    }

};
