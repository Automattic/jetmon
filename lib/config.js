var config = {};

config.mysql = {};
config.mysql.host     = 'localhost';
config.mysql.user     = 'root';
config.mysql.password = '',
config.mysql.database = 'jetmon',

config.bucket_no_min       = 0;
config.bucket_no_max       = 512;
config.batch_size          = 256;

config.numWorkers          = 200; // Number of Workers to create
config.NUM_TO_PROCESS      = 10; // Number of simultaneous connections per worker

config.NUM_OF_CHECKS       = 3;
config.TIME_BETWEEN_CHECKS = 20; //seconds

config.HTTP_PORT           = 80;

config.sendmails           = false;

config.mailer = {};
config.mailer.from = '';
config.mailer.host = '';
config.mailer.port = '';
config.mailer.user = '';
config.mailer.password = '';


module.exports = config;