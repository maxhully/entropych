/* All timestamp columns are unix timestamps in UTC */

create table if not exists upload (
    upload_id integer primary key,
    created_at integer not null, /* unix timestamp */
    filename text not null unique,
    content_type text not null,
    contents blob not null
);

create table if not exists user (
    user_id integer primary key,
    user_name text not null,
    password_hash blob,
    password_salt blob,
    display_name text,
    bio text,
    avatar_upload_id integer references upload (upload_id)
);
create unique index if not exists user_user_name_uniq_idx on user (user_name);

create table if not exists post (
    post_id integer primary key,
    user_id integer references user(user_id),
    created_at integer not null, /* unix timestamp */
    content text not null
);
create index if not exists post_user_id_idx on post (user_id);
create index if not exists post_created_at_idx on post (created_at);

create table if not exists post_reply (
    post_id integer references post(post_id),
    reply_post_id integer references post(post_id),
    primary key (post_id, reply_post_id)
);

create table if not exists reaction (
    post_id integer not null,
    user_id integer not null,
    reacted_at integer not null,
    emoji text not null,
    primary key (post_id, user_id)
);

create table if not exists user_session (
    user_session_id integer primary key,
    user_id integer references user(user_id),
    session_public_id blob not null, /* random string we can put in a cookie */
    created_at integer not null, /* unix timestamp */
    expiration_time integer not null /* unix timestamp */
);
create index if not exists user_session_session_public_id_idx on user_session (session_public_id);

create table if not exists user_follow (
    /* I didn't call this follower_user_id because it's only one letter away from followed_user_id,
    and that sounds confusing. Plus this means we can join to use `using (user_id)` to get all the
    users you follow. */
    user_id integer not null references user(user_id), /* the following user */
    followed_user_id integer not null references user(user_id), /* the followed user */
    followed_at integer not null, /* unix timestamp */
    primary key (user_id, followed_user_id)
);
