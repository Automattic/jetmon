
#ifndef __HTTP_CHECKER_H__
#define __HTTP_CHECKER_H__

#include <iostream>
#include <sys/time.h>

#include <QTcpSocket>
#include <QSslSocket>
#include <QTimer>

#include "headers/config.h"

#define DEFAULT_HTTP_PORT   80
#define DEFAULT_HTTPS_PORT 443

struct HealthCheck {
	int blog_id;
	QString monitor_url;
	QString jetmon_server;
	QDateTime received;
	int thread_index;
};

class HTTP_Checker: public QObject
{
	Q_OBJECT
public:
	HTTP_Checker( const int p_net_timeout = 20000 );
	~HTTP_Checker();

	void check( HealthCheck* hc );
	int get_rtt() { return m_starttime.msecsTo( QDateTime::currentDateTime() ); }
	int get_response_code() { return m_response_code; }

signals:
	void finished( HTTP_Checker* checker, HealthCheck* hc );

private slots:
	void connected();
	void connectionError( QAbstractSocket::SocketError err );
	void readyRead();
	void timed_out();

private:
	QSslConfiguration *m_ssl_config;
	QAbstractSocket *m_sock;
	HealthCheck *m_hc;
	QTimer *m_timeout;

	QString m_host_name;
	QString m_host_dir;
	int m_port;
	bool m_is_ssl;
	bool m_finished;
	QDateTime m_starttime;
	QString m_response;
	int m_redirects;
	int m_response_code;
	int m_net_timeout;

	void connect();
	void closeConnection();
	bool send_http_get();
	void process_response();
	void finish_request();
	bool set_redirect_host_values( QString p_content );
	void parse_host_values();
	void parse_response_code( QByteArray a_data );
};

#endif	//__HTTP_H__

