
#ifndef __CLIENT_THREAD_H__
#define __CLIENT_THREAD_H__

#include <QRunnable>
#include <QtNetwork/QAbstractSocket>
#include <QtNetwork/QSslSocket>
#include <QtNetwork/QTcpSocket>
#include <QJsonValue>
#include <QDateTime>
#include <QString>
#include <QHostAddress>
#include <QUrl>
#include <QVector>

#include "headers/config.h"
#include "headers/check_controller.h"

#define HOST_DOWN           0
#define HOST_ONLINE         1

class ClientThread : public QRunnable
{
public:
	enum QueryType { ServiceRunning, SiteStatusCheck, SiteStatusPostCheck, UnknownQuery };

	ClientThread( qintptr sock,
					const QSslConfiguration *ssl_config,
					CheckController *checker,
					const QString &veriflier_name,
					const QString &auth_token,
					const int net_timeout,
					const bool debug );
	~ClientThread();

	void run();

private:
	qintptr m_sock;
	QSslSocket *m_socket;
	const QSslConfiguration *m_ssl_config;
	CheckController *m_checker;
	QVector<HealthCheck*> m_checks;

	QString m_veriflier_name;
	QString m_auth_token;
	int m_net_timeout;
	QString m_jetmon_server;

	bool m_debug;
	bool m_site_status_request;

	void sendOK();
	void sendServiceOK();
	void sendError( const QString errorString );

	void readRequest();

	QueryType get_request_type( QByteArray &raw_data );

	QJsonDocument parse_json_request( QByteArray &raw_data );
	QJsonDocument parse_json_request_post( QByteArray &raw_data );

	int parse_json_request_post_length( QByteArray &raw_data );
	int get_content_length( QByteArray &raw_data );

	bool parse_requests( QueryType type, QJsonDocument json_doc );

	QString get_http_reply_header( const QString &http_code, const QString &p_data);
	QString get_http_content( int status, const QString &error = "" );
};
#endif // __CLIENT_THREAD_H__

