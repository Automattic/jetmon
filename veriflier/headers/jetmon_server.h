
#ifndef __JETMON_SERVER_H__
#define __JETMON_SERVER_H__

#include <QObject>
#include <QtNetwork/QSslSocket>
#include <QJsonDocument>
#include <QJsonObject>

#include "headers/logger.h"

class JetmonServer : public QObject
{
	Q_OBJECT
public:
	JetmonServer( QObject *parent, const QSslConfiguration *ssl_config, QString jetmon_server, int jetmon_server_port );

	void sendData( QByteArray status_data );
	QString jetmonServer() { return m_jetmon_server; }

signals:
	void finished( JetmonServer* jetmon_server, int status, int rtt );

private slots:
	void connected();
	void connectionError( QAbstractSocket::SocketError err );
	void readyRead();

private:
	QSslSocket *m_socket;
	QString m_jetmon_server;
	int m_jetmon_server_port;
	QDateTime m_timer;
	QByteArray m_status_data;

	QJsonDocument parse_json_response( QByteArray &raw_data );
	void closeConnection();
};

#endif // __JETMON_SERVER_H__
