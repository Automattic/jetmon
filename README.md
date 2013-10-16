jetmon.js
=========

Overview
--------

Parallel HTTP health monitoring using HEAD requests for large scale website monitoring.

Takes a file with domains, each on a new line, as input and displays summary sites per second output.

Installation
------------

1) Install node.js
2) Install mysql and nodemailer npm modules
2) Run "node-gyp rebuild" in the application root directory.

Running
-------

Run jetmon with "node lib/jetmon.js" in the application root directory.

Database
-------

Main Table Schema:

CREATE TABLE `main_table` (
  `blog_id` bigint(20) unsigned NOT NULL,
  `bucket_no` smallint(2) unsigned NOT NULL DEFAULT '1',
  `monitor_status` tinyint(1) unsigned NOT NULL DEFAULT '1',
  `url` varchar(300) NOT NULL DEFAULT '',
  `site_status` tinyint(1) unsigned NOT NULL DEFAULT '1',
  `last_status_change_time` timestamp NULL DEFAULT NULL,
  `email_addresses` text,
  PRIMARY KEY (`blog_id`,`bucket_no`)
)
