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
  PRIMARY KEY (`blog_id`,`bucket_no`)
)
