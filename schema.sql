create table if not exists user (
    user_id integer primary key not null,
    name text not null
);

create table if not exists post (
    post_id integer primary key not null,
    user_id integer references user(user_id),
    created_at integer not null,
    content text not null
);
