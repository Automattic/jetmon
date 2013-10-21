var config  = require( './config' );

var nodemailer = require("nodemailer");

var smtpTransport = nodemailer.createTransport("SMTP",{
    host: config.mailer.host,
    port: config.mailer.port,
    auth: {
        user: config.mailer.user,
        pass: config.mailer.password
    }
});

var jetmonmailer = {

    send_mail : function ( server ) {

        var mailOptions = {
            from: config.mailer.from,
            subject: jetmonmailer.get_subject( server ), // Subject line
            to: jetmonmailer.get_email_addresses( server ),
            text: jetmonmailer.get_email_text( server ), // plaintext body
            html: jetmonmailer.get_email_html( server ) // html body
        };

        smtpTransport.sendMail( mailOptions, function( error, response ) {
            if ( error ){
                console.log( error );
            } else {
                console.log( "Message sent: " + response.message );
            }
        });
    },

    get_subject : function ( server ) {
        var txt = server.site_status?"Site Up":"Site Down"
        return "SUBJECT: " + txt;
    },

    get_email_addresses : function ( server ) {
        return server.email_addresses;
    },

    get_email_text : function ( server ) {
        var txt = server.site_status?"Site Up":"Site Down"
        return "PLAIN TEXT: " + txt;
    },

    get_email_html : function ( server ) {
        var txt = server.site_status?"Site Up":"Site Down"
        return "HTML EMAIL: " + txt;
    }

};
