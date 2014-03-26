
#ifndef __CLIENT_THREAD_H__
#define __CLIENT_THREAD_H__

#include <QRunnable>
#include <QtNetwork/QAbstractSocket>
#include <QtNetwork/QSslSocket>
#include <QJsonValue>
#include <QDateTime>
#include <QString>
#include <QHostAddress>
#include <QUrl>

#include "headers/config.h"
#include "headers/http_checker.h"

#define HOST_DOWN           0
#define HOST_ONLINE         1

class ClientThread : public QRunnable
{
public:
	enum QueryType { ServiceRunning, SiteStatusCheck, UnknownQuery };

	ClientThread( qintptr p_sock, const QString &p_veriflier_name = "",
					const QString &p_auth_token = "",
					const int p_net_comms_timeout = 20000,
					const bool p_debug = false );
	~ClientThread();

	void run();

private:
	qintptr m_sock;
	QSslSocket *m_socket;

	QString m_jetmon_server;
	QString m_veriflier_name;
	QString m_auth_token;
	QJsonValue m_monitor_url;
	QJsonValue m_blog_id;
	int m_net_comms_timeout;

	QDateTime timer;
	bool m_running;
	bool m_debug;
	bool m_site_status_request;

	void sendOK();
	void sendServiceOK();
	void sendResult( int status );
	void sendError( const QString errorString );

	void readRequest();
	void readResponse();
	void performHostCheck();

	QueryType get_request_type( QByteArray &raw_data );

	QJsonDocument parse_json_request( QByteArray &raw_data );
	QJsonDocument parse_json_response( QByteArray &raw_data );

	QString get_http_reply_header( const QString &http_code, const QString &p_data);
	QString get_http_content( int status, const QString &error = "" );
	QString get_http_request_header( int status );
	QString get_http_request_content( int status );
};
#endif // __CLIENT_THREAD_H__

