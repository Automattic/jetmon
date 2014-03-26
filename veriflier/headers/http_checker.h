
#ifndef __HTTP_CHECKER_H__
#define __HTTP_CHECKER_H__

#include <iostream>
#include <sys/time.h>

#include <QTcpSocket>
#include <QSslSocket>
#include <QThread>

#include "headers/config.h"

#define DEFAULT_HTTP_PORT   80
#define DEFAULT_HTTPS_PORT 443

class HTTP_Checker: QObject {
	Q_OBJECT
public:
	HTTP_Checker( const int p_net_comms_timeout );
	~HTTP_Checker();

	void check( QString p_host_name );
	time_t get_rtt() { return m_triptime; }
	int get_response_code() { return m_response_code; }

private:
	QTcpSocket *m_sock;
	QSslSocket *m_ssl;

	QString m_host_name;
	QString m_host_dir;
	int m_port;
	struct timezone m_tzone;
	time_t m_triptime;
	int m_response_code;
	int m_net_comms_timeout;

	bool connect();
	bool closeConnection();
	QString send_http_get();
	bool send_bytes( QString s_data );
	QString get_response();
	void set_host_response( int redirects );
	bool set_redirect_host_values( QString p_content );
	void parse_host_values();
};

#endif	//__HTTP_H__

