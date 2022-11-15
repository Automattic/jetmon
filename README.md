jetmon.js
=========

Overview
--------

Parallel HTTP health monitoring using HEAD requests for large scale website monitoring.

The service relies on confirmation from external servers to verify that sites are indeed offline. This mitigates the Internet weather issue sometimes giving false positives. The code for these servers can be found in the verifliers directory.

Architecture
--------
![jetmon_chart](https://user-images.githubusercontent.com/1758399/201877599-8992b68a-9ca7-4984-9de7-abe99f989d88.png)

Jetmon will periodically (every 5 minutes) loop over a list of Jetpack sites and perform a HEAD request to check their current status.

When a status change is detected, Jetmon will notify WPCOM including the related notification data in the request.

Here are the possible flows, depending on the status change:

| Previous Status  | Current status   | Action                                                                             |
| ---------------- | ---------------- | ---------------------------------------------------------------------------------- |
| DOWN             | UP               | Notify WPCOM about status change                                                   |
| UP               | DOWN             | Verify status down via the Veriflier services and notify WPCOM about status change |
| DOWN             | DOWN (confirmed) | Notify WPCOM about status change                                                   |

### Jetmon service

The Jetmon master service is responsible for communicating with the database in order to fetch a list of sites to check. It will spawn and re-allocate workers every five seconds and update stats repeatedly based on `STATS_UPDATE_INTERVAL_MS`.

The jetmon-workers internally use an Node Addon written in C++ to check the connection by sending a HEAD request to the server. 


### Verifliers

The Veriflier service, which is written in C++ and uses the QT Framework, does something similar to the Node Addon mentioned before, but lives in its own server. Note that the production environment consists of multiple Verifliers, though the local development environment consists of a single Veriflier service.

### Notification data

Here are the current notification data, Jetmon sends to WPCOM upon detecting a site status change:
- `blog_id`: The site's WPCOM ID
- `status_id`: The site's current status. Enum: `0` is status down, `1` is status running and `2` status confirmed down.
- `last_check`: The datetime of the last check
- `last_status_change`: The datetime of the last status change
- `checks`: An array of the checks results from both Jetmon and Veriflier services. Each entry consists of:
    - `type`: Enum: `1` refers to a Jetmon check, while `2` to a Veriflier check.
    - `host`: The server hostname.
    - `status`: The site's current status. Enum: `0` is status down, `1` is status running and `2` status confirmed down.
    - `rtt`: Round-trip time (RTT) in milliseconds (ms).
    - `code`: The HTTP response status code.


Installation
------------

1) Make sure you have installed [Docker](https://docs.docker.com/get-docker/) and [docker-compose](https://docs.docker.com/compose/install/)

2) Clone the Jetmon monorepo

3) Copy the environment variables file from within the `docker` folder: `cp jetmon/docker/.env-sample jetmon/docker/.env`

4) Open `jetmon/docker/.env` and make any modifications you'd like.

5) Run `docker compose build` from within the `docker` folder

Configuration
-------------

The Jetmon configuration lives under `config/config.json`. This file is generated on the fly, if not present, each time you run the Jetmon service, using the `config-sample.json` and the corresponding environment variables defined in `docker/.env`.
Feel free to modify your local config file as needed.

The Veriflier configuration lives under `veriflier/config/veriflier.json`. This file is generated on the fly, if not present, each time you run the Veriflier service, using the `veriflier-sample.json` and the corresponding environment variables defined in `docker/.env`. 

Running
-------

Run `docker compose up -d` from within the `docker` folder.

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

