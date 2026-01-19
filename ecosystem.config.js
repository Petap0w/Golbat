module.exports = {
  apps: [
    {
      name: 'golbat',
      script: './golbat',
      instances: 1,
      exec_mode: 'fork',
      env: {
        // Add any environment variables here if needed
      },
      max_memory_restart: '60G',  // Current usage: 55GB, set to 60GB for safety
      error_file: './logs/golbat-error.log',
      out_file: './logs/golbat-out.log',
      log_date_format: 'YYYY-MM-DD HH:mm:ss Z',
      merge_logs: true,
      autorestart: true,
      watch: false,
      max_restarts: 10,
      min_uptime: '10s',
      restart_delay: 4000
    },
    {
      name: 'golbat-writer',
      script: './golbat-writer',
      instances: 1,
      exec_mode: 'fork',
      env: {
        // Number of workers is controlled via config.toml (writer_workers)
      },
      max_memory_restart: '4G',
      error_file: './logs/golbat-writer-error.log',
      out_file: './logs/golbat-writer-out.log',
      log_date_format: 'YYYY-MM-DD HH:mm:ss Z',
      merge_logs: true,
      autorestart: true,
      watch: false,
      max_restarts: 10,
      min_uptime: '10s',
      restart_delay: 4000
    }
  ]
};

