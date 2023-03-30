DEBUG
Set to true to enable more verbose log messages in logs/jetmon.log.

NUM_WORKERS
The number of forked worker processes to create and maintain.

NUM_TO_PROCESS
The number of sites that a worker should process in parallel.

DATASET_SIZE
The maximum number of sites to send to a worker's queue in a single batch.

DB_UPDATES_ENABLE
WARNING: Do not enabled this on production hosts. This should only be enabled on local docker test environments and never in production.
Set to true to allow Jetmon to update the jetpack_monitor_sites table. Without this, it is difficult to test how effective the code is working when in a local docker test environment.

BUCKET_NO_MIN
The first bucket in the range of jetpack_monitor_sites buckets that this host should process when checking sites. Each host should be configured to have a unique set of buckets that it is responsible for.
The buckets currently range from 0 to 511.

BUCKET_NO_MAX
The last bucket in the range of jetpack_monitor_sites buckets that this host should process when checking sites. Each host should be configured to have a unique set of buckets that it is responsible for.
The buckets currently range from 0 to 511.

BATCH_SIZE
The number of buckets returned in each batch when running checks.

AUTH_TOKEN
A string used to validate communications between different systems over HTTPS.

VERIFLIER_BATCH_SIZE
The maximum number of sites to send to verifliers in a single batch.

SQL_UPDATE_BATCH
Unknown. Likely not used currently.

DB_CONFIG_UPDATES_MIN
How frequently in minutes the database library should check for DB config changes in order to reload.

PEER_OFFLINE_LIMIT
The minimum number of verifliers that must confirm that a site is down before changing the site status to down.

NUM_OF_CHECKS
The number of local checks that must fail before a site is checked by the verifliers.

TIME_BETWEEN_CHECKS_SEC
The minimum amount of time that must elapse between local checks for a specific site.

STATS_UPDATE_INTERVAL_MS
The minimum delay, in milliseconds, between stats updates to both statsd and stats log files.

TIME_BETWEEN_NOTICES_MIN
The minimum delay, in minutes, that must pass before a site can transition from SITE_DOWN to SITE_CONFIRMED_DOWN.

MIN_TIME_BETWEEN_ROUNDS_SEC
The minimum delay, in seconds, between check rounds.
Note: This value has no effect if USE_VARIABLE_CHECK_INTERVALS is set to true.

TIMEOUT_FOR_REQUESTS_SEC
The amount of time, in seconds, that a site can remain in the queuedRetries array (the queue that holds sites being checked by verifliers) before being purged out of the queue.

USE_VARIABLE_CHECK_INTERVALS
Set to true to enable the variable check intervals as set for each site in the jetpack_monitor_sites table.
Note: Enabling this disables use of the MIN_TIME_BETWEEN_ROUNDS_SEC config, sets the round loop to execute every minute, and checks each site on the interval as set in the database.
