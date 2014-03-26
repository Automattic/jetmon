
#include "headers/config.h"
#include "headers/logger.h"

Config *Config::m_instance = new Config;

Config::Config() {
	load_config_file();
}

void Config::load_config_file() {
	QFile file;
	file.setFileName( "./config/veriflier.json" );
	file.open( QIODevice::ReadOnly | QIODevice::Text );
	QString val = file.readAll();
	file.close();
	m_json = QJsonDocument::fromJson( val.toUtf8() );
}

int Config::get_int_value( QString name ) {
	if ( m_json.isEmpty() || m_json.isNull() )
		return -1;

	QJsonValue value = m_json.object().value( name );
	if ( value.isNull() ) {
		LOG( ( QString( "Missing '" ) + name + QString( "' JSON value in config file." ) ).toStdString().c_str() );
		return -1;
	}
	return value.toInt();
}

bool Config::get_bool_value( QString name ) {
	if ( m_json.isEmpty() || m_json.isNull() )
		return false;

	QJsonValue value = m_json.object().value( name );
	if ( value.isNull() ) {
		LOG( ( QString( "Missing '" ) + name + QString( "' JSON value in config file." ) ).toStdString().c_str() );
		return false;
	}
	return value.toBool();
}

QString Config::get_string_value( QString name ) {
	if ( m_json.isEmpty() || m_json.isNull() )
		return QString( "" );

	QJsonValue value = m_json.object().value( name );
	if ( value.isNull() ) {
		LOG( ( QString( "Missing '" ) + name + QString( "' JSON value in config file." ) ).toStdString().c_str() );
		return QString( "" );
	}
	return QString( value.toString() );
}

