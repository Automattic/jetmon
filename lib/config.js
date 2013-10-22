var config = {};

//DATABASE SETTINGS
config.mysql = {};
config.mysql.read = {};
config.mysql.read.HOST     = 'localhost';
config.mysql.read.PORT     = 3306;
config.mysql.read.USER     = '';
config.mysql.read.PASSWORD = '',
config.mysql.read.DATABASE = '',

config.mysql.write = {};
config.mysql.write.HOST     = 'localhost';
config.mysql.write.PORT     = 3306;
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

config.NUM_OF_CHECKS       = 3; // Number of checks to perform to confirm that a site is down
config.TIME_BETWEEN_CHECKS = 5000; // milliseconds between checks

config.MIN_TIME_BETWEEN_ROUNDS = 200000; // milliseconds between two rounds of checks
config.HTTP_PORT           = 80;

//NOTIFICATION SETTINGS
config.sendmails           = false;
config.mailer = {};
config.mailer.FROM = '';
config.mailer.HOST = '';
config.mailer.PORT = '';
config.mailer.USER = '';
config.mailer.PASSWORD = '';


module.exports = config;