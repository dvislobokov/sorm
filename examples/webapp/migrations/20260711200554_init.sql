-- sorm migration: init
-- create "users" table
CREATE TABLE "public"."users" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "name" text NOT NULL, "email" text NOT NULL, "version" bigint NOT NULL, PRIMARY KEY ("id"));
-- create index "users_email_key" to table: "users"
CREATE UNIQUE INDEX "users_email_key" ON "public"."users" ("email");
