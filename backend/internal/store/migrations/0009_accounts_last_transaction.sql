ALTER TABLE accounts ADD COLUMN last_transaction_id INTEGER REFERENCES transactions(id);
