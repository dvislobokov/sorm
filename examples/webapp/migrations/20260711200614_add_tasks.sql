-- sorm migration: add_tasks
-- create "tasks" table
CREATE TABLE "public"."tasks" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "user_id" bigint NOT NULL, "title" text NOT NULL, "done" boolean NOT NULL, "created_at" timestamptz NOT NULL, "version" bigint NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "tasks_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id"));
