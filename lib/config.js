var SECONDS = 1000;
var MINUTES = 60 * SECONDS;
var HOURS = 60 * MINUTES;
var DAYS = 24 * HOURS;

var config = {};

//DATABASE SETTINGS
config.mysql = {};
config.mysql.read = {};
config.mysql.read.HOST     = '';
config.mysql.read.PORT     = 0;
config.mysql.read.USER     = '';
config.mysql.read.PASSWORD = '',
config.mysql.read.DATABASE = '',

config.mysql.write = {};
config.mysql.write.HOST     = '';
config.mysql.write.PORT     = 0;
config.mysql.write.USER     = '';
config.mysql.write.PASSWORD = '',
config.mysql.write.DATABASE = '',

//MONITOR SETTINGS
config.DEBUG = true;

config.BUCKET_NO_MIN       = 0;
config.BUCKET_NO_MAX       = 512;
config.BATCH_SIZE          = 32;

config.NUM_WORKERS          = 40; // Number of Workers to create
config.NUM_TO_PROCESS      = 20; // Number of simultaneous connections per worker
config.BUCKET_SIZE         = 100;

config.NUM_OF_CHECKS       = 2; // Number of checks to perform to confirm that a site is down
config.TIME_BETWEEN_CHECKS = 1 * MINUTES; // milliseconds between checks

config.TIME_BETWEEN_NOTIFICATIONS = 1 * HOURS; // milliseconds between checks

config.MIN_TIME_BETWEEN_ROUNDS = 3 * MINUTES; // milliseconds between two rounds of checks
config.HTTP_PORT           = 80;

//NOTIFICATION SETTINGS
config.sendmails           = true;
config.mailer = {};
config.mailer.FROM = '';
config.mailer.HOST = '';
config.mailer.PORT = '';
config.mailer.USER = '';
config.mailer.PASSWORD = '';

config.mailer.serverDownSubject = 'We noticed that your site %url% was down!';

config.mailer.serverDownHTML = '\r\n\
Hi there,\r\n\
\r\n\
Jetpack Monitor is on the job, keeping tabs on %url%. During our last check on %date_and_time%, we noticed that your site was down.\r\n\
\r\n\
If you’re concerned about your site’s status, you might want to get in touch with your hosting provider. We’ll continue keeping track, and will let you know when your site is up and running again and the total downtime.\r\n\
\r\n\
Cheers,\r\n\
The Jetpack Team\r\n\
';

config.mailer.serverStillDownSubject = 'Bad news — your site %url% is still down!'

config.mailer.serverStillDownHTML = '\r\n\
Hi there,\r\n\
\r\n\
We’e following up on the recent Jetpack Monitor alert we sent. It appears that your site is still down, and has been for %downtime%.\r\n\
\r\n\
At this point, you’ll probably want to reach out to your hosting provider to determine the cause of the outage. Feel free to contact us as well; if there’s something we can to do help, we’re happy to lend a hand.\r\n\
\r\n\
We’ll continue monitoring your site, and will let you know when it’s up again.\r\n\
\r\n\
Cheers,\r\n\
The Jetpack Team\r\n\
';

config.mailer.serverUpSubject = 'Good news — your site %url% is back up!';

config.mailer.serverUpHTML = '\r\n\
Hi there,\r\n\
\r\n\
Good news — your site %url% is back up!\r\n\
\r\n\
Your total downtime was %downtime%, but your site was up again as of %date_and_time%.\r\n\
\r\n\
If it goes down again at at point (we hope it doesn’t!), we’ll be in touch.\r\n\
\r\n\
Cheers,\r\n\
The Jetpack Team\r\n\
';

module.exports = config;
