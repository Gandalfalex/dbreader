-- Runs once, when the container first creates its database, as SYS in the root.
-- Everything lives in the APP schema of FREEPDB1, which the container creates.
ALTER SESSION SET CONTAINER = FREEPDB1;
ALTER SESSION SET CURRENT_SCHEMA = app;

CREATE TABLE customers (
  id         NUMBER(10) GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name       VARCHAR2(100) NOT NULL,
  email      VARCHAR2(200),
  created_at DATE DEFAULT SYSDATE NOT NULL,
  notes      CLOB
);

CREATE TABLE orders (
  id          NUMBER(10) GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  customer_id NUMBER(10) NOT NULL REFERENCES customers (id),
  total       NUMBER(12, 2) NOT NULL,
  placed_at   TIMESTAMP DEFAULT SYSTIMESTAMP NOT NULL,
  status      VARCHAR2(20) DEFAULT 'new' NOT NULL
);

CREATE TABLE attachments (
  id       NUMBER(10) GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id NUMBER(10) NOT NULL REFERENCES orders (id),
  filename VARCHAR2(200) NOT NULL,
  content  BLOB
);

CREATE TABLE "MixedCase" ("Key" VARCHAR2(10), "value" VARCHAR2(10));

CREATE VIEW open_orders AS SELECT * FROM orders WHERE status <> 'done';

INSERT INTO customers (name, email, notes) VALUES ('Ada Lovelace', 'ada@example.com', 'Wrote the first program.');
INSERT INTO customers (name, email) VALUES ('Bob Builder', 'bob@example.com');
INSERT INTO customers (name, email, notes) VALUES ('Cy Young', NULL, 'Prefers phone; never email.');
INSERT INTO orders (customer_id, total, status) VALUES (1, 120.50, 'done');
INSERT INTO orders (customer_id, total) VALUES (1, 19.99);
INSERT INTO orders (customer_id, total, status) VALUES (2, 7.25, 'shipped');
INSERT INTO attachments (order_id, filename, content) VALUES (1, 'invoice.pdf', HEXTORAW('255044462D312E34'));
INSERT INTO "MixedCase" VALUES ('a', 'b');
COMMIT;
EXIT;
