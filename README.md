jetmon.js
=========

Overview
--------

Parallel HTTP health monitoring using HEAD requests for large scale website monitoring.

The service relies on confirmation from external servers to verify that sites are indeed offline. This mitigates the Internet weather issue sometimes giving false positives. The code for these servers can be found in the verifliers directory.

Installation
------------

1) Install node.js.

2) Install the mysql npm module with 'npm install mysql'.

3) Ensure you have node-gyp, if not 'npm install -g node-gyp'.

4) Run "node-gyp rebuild" in the application root directory.

5) You will need to follow the instruction in the veriflier directory to build the verification servers.

Configuration
-------------

The service support multi datacenter config, therefore to first step to get the service up and running is to set the datacenter in the config file. Whatever the configured datacenter name is, it will need to have matching entries in the db-config.conf file (see column definitions of the config array in dbpools.js). Only read servers are required by the jetmon service.

The setup of the verification servers is straight forward, just be sure to specify tokens for each service and ensure they each have the others token setup on them. For example, the "Veriflier 1" 'auth_token', which you set in the jetmon config, must match the 'auth_token' in the 'veriflier.json' file on "Veriflier 1".

Running
-------

Run jetmon with "node lib/jetmon.js" in the application root directory.

Database
-------

Main Table Schema:

	CREATE TABLE `jetpack_monitor_subscription` (
		`blog_id` bigint(20) unsigned NOT NULL,
		`bucket_no` smallint(2) unsigned NOT NULL DEFAULT 1,
		`monitor_url` varchar(300) NOT NULL,
		`monitor_active` tinyint(1) unsigned NOT NULL DEFAULT 1,
		`site_status` tinyint(1) unsigned NOT NULL DEFAULT 1,
		`last_status_change` timestamp NULL DEFAULT NULL,
		PRIMARY KEY (`blog_id`)
	);
	CREATE INDEX `bucket_no_monitor_active` ON `jetpack_monitor_subscription` (`bucket_no`, `monitor_active`);

