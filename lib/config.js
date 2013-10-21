var config = {};

//DATABASE SETTINGS
config.mysql = {};
config.mysql.host     = 'localhost';
config.mysql.user     = 'root';
config.mysql.password = '',
config.mysql.database = 'jetmon',

//MONITOR SETTINGS
config.bucket_no_min       = 0;
config.bucket_no_max       = 512;
config.batch_size          = 32;

config.numWorkers          = 40; // Number of Workers to create
config.NUM_TO_PROCESS      = 20; // Number of simultaneous connections per worker

config.NUM_OF_CHECKS       = 3; // Number of checks to perform to confirm that a site is down
config.TIME_BETWEEN_CHECKS = 20000; // milliseconds between checks

config.MIN_TIME_BETWEEN_ROUNDS = 300000; // milliseconds between two rounds of checks
config.HTTP_PORT           = 80;

//NOTIFICATION SETTINGS
config.sendmails           = false;
config.mailer = {};
config.mailer.from = '';
config.mailer.host = '';
config.mailer.port = '';
config.mailer.user = '';
config.mailer.password = '';


module.exports = config;