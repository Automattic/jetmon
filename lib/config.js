
const SECONDS = 1000;
const MINUTES = 60 * SECONDS;
const HOURS   = 60 * MINUTES;
const DAYS    = 24 * HOURS;

var config = new Object();

// Monitor Settings
config.DEBUG = true;

config.HTTP_PORT     = 80;
config.BUCKET_NO_MIN = 0;
config.BUCKET_NO_MAX = 512;
config.BATCH_SIZE    = 32;

config.NUM_WORKERS       = 40;
config.NUM_TO_PROCESS    = 20; 	// simultaneous connections per worker
config.DATASET_SIZE      = 100;
config.SQL_UPDATE_BATCH  = 1;	// setting to be used for batching DB Updates

config.NUM_OF_CHECKS         = 2;            // Number of checks to perform to confirm that a site is down
config.TIME_BETWEEN_CHECKS   = 20 * SECONDS;  // milliseconds between confirmation checks
config.STATS_UPDATE_INTERVAL = 1000;

config.TIME_BETWEEN_NOTIFICATIONS = 5 * MINUTES;   // milliseconds between user site down notifications
config.MIN_TIME_BETWEEN_ROUNDS    = 5 * MINUTES; // milliseconds between two rounds of checks

// Notification Settings
config.sendmails       = true;
config.mailer          = new Object();
config.mailer.FROM     = 'Jetpack Support <support@jetpack.me>';
config.mailer.HOST     = '127.0.0.1';
config.mailer.PORT     = '25';
config.mailer.USER     = '';
config.mailer.PASSWORD = '';

config.templates       = new Object();
config.templates.TEMPLATES_DIR = './lib/templates/';

module.exports = config;
