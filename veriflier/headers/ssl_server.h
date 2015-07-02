
#ifndef __SSL_SERVER_H__
#define __SSL_SERVER_H__

#include <QTcpServer>
#include <QThreadPool>
#include <QSslConfiguration>
#include <QSslCertificate>
#include <QSslKey>

#include "headers/config.h"
#include "headers/client_thread.h"
#include "headers/check_controller.h"

#define DEFAULT_MAX_CHECKS 500

class SSL_Server : public QTcpServer
{
	Q_OBJECT
public:
	SSL_Server( QObject *parent = 0 );
	~SSL_Server();

protected:
	void incomingConnection( qintptr socketDescriptor );

public slots:
	void logError( QAbstractSocket::SocketError socketError );

private:
	QThreadPool *pool;
	CheckController *m_checker;
	QSslConfiguration *m_ssl_config;

	QDateTime ticker;
	QString m_veriflier_name;
	QString m_auth_token;
	int m_net_timeout;
	int m_served_count;
	int m_jetmon_server_port;
	bool m_debug;
};

#endif // __SSL_SERVER_H__

