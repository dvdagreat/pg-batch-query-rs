import { Client } from 'pg';

export function createClient(): Client {
  return new Client({
    host: 'localhost',
    user: 'postgres',
    password: 'password',
    database: 'postgres',
  });
}
