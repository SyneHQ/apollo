#!/usr/bin/env bun

import { Database } from 'bun:sqlite';
import { createClient } from 'redis';

// Types for job parameters
interface AnalyticsJobParams {
  queryType: string;
  database: string;
  outputFormat: string;
  parallelism: number;
  executionId: string;
  userId: string;
}

// Parse command line arguments
function parseArgs(): AnalyticsJobParams {
  const args = process.argv.slice(3); // Skip 'bun', 'index.ts', and 'analytics'
  const params: Partial<AnalyticsJobParams> = {};
  
  for (let i = 0; i < args.length; i += 2) {
    const key = args[i]?.replace('--', '');
    const value = args[i + 1];
    
    if (key && value) {
      switch (key) {
        case 'query-type':
          params.queryType = value;
          break;
        case 'database':
          params.database = value;
          break;
        case 'output-format':
          params.outputFormat = value;
          break;
        case 'parallelism':
          params.parallelism = parseInt(value);
          break;
      }
    }
  }
  
  // Get environment variables
  params.executionId = process.env.EXECUTION_ID || 'unknown';
  params.userId = process.env.USER_ID || 'unknown';
  
  return params as AnalyticsJobParams;
}

// Database connection
async function getDatabaseConnection(): Promise<Database> {
  const databasePath = process.env.DATABASE_PATH || '/app/analytics.db';
  const db = new Database(databasePath);
  
  // Enable WAL mode for better concurrency
  db.exec('PRAGMA journal_mode = WAL');
  
  return db;
}

// Redis connection
async function getRedisConnection() {
  const redisUrl = process.env.REDIS_URL;
  if (!redisUrl) {
    console.log('No REDIS_URL provided, skipping Redis operations');
    return null;
  }
  
  const client = createClient({
    url: redisUrl,
  });
  
  await client.connect();
  return client;
}

// Analytics job execution
async function executeAnalyticsJob(params: AnalyticsJobParams): Promise<any> {
  console.log('🚀 Starting analytics job with parameters:', params);
  console.log('📊 Environment variables:');
  console.log('  - DATABASE_PATH:', process.env.DATABASE_PATH || '/app/analytics.db');
  console.log('  - API_KEY:', process.env.API_KEY ? '***configured***' : 'not set');
  console.log('  - REDIS_URL:', process.env.REDIS_URL ? '***configured***' : 'not set');
  console.log('  - LOG_LEVEL:', process.env.LOG_LEVEL || 'info');
  
  const db = await getDatabaseConnection();
  const redis = await getRedisConnection();
  
  try {
    let result: any;
    
    switch (params.queryType) {
      case 'export':
        result = await executeExportJob(db, params);
        break;
      case 'report':
        result = await executeReportJob(db, params);
        break;
      case 'aggregation':
        result = await executeAggregationJob(db, params);
        break;
      default:
        throw new Error(`Unknown query type: ${params.queryType}`);
    }
    
    // Cache result in Redis if available
    if (redis) {
      const cacheKey = `analytics:${params.executionId}`;
      await redis.setEx(cacheKey, 3600, JSON.stringify(result));
      console.log('✅ Result cached in Redis');
    }
    
    // Send notification if API_KEY is available
    if (process.env.API_KEY) {
      await sendNotification(params, result);
    }
    
    return result;
    
  } finally {
    db.close();
    if (redis) {
      await redis.disconnect();
    }
  }
}

// Export job implementation
async function executeExportJob(db: Database, params: AnalyticsJobParams): Promise<any> {
  console.log(`📤 Executing export job for database: ${params.database}`);
  
  // Simulate database query - SQLite compatible
  const query = `
    SELECT 
      id,
      name,
      created_at,
      updated_at
    FROM users 
    WHERE created_at >= datetime('now', '-7 days')
    ORDER BY created_at DESC
    LIMIT 1000
  `;
  
  const result = db.query(query).all();
  
  const exportData = {
    queryType: params.queryType,
    database: params.database,
    outputFormat: params.outputFormat,
    recordCount: result.length,
    executionId: params.executionId,
    userId: params.userId,
    timestamp: new Date().toISOString(),
    data: result
  };
  
  console.log(`✅ Export completed: ${result.length} records`);
  return exportData;
}

// Report job implementation
async function executeReportJob(db: Database, params: AnalyticsJobParams): Promise<any> {
  console.log(`📊 Executing report job for database: ${params.database}`);
  
  // Simulate report generation - SQLite compatible
  const queries = [
    'SELECT COUNT(*) as total_users FROM users',
    'SELECT COUNT(*) as active_users FROM users WHERE last_login >= datetime(\'now\', \'-30 days\')',
    'SELECT COUNT(*) as new_users FROM users WHERE created_at >= datetime(\'now\', \'-7 days\')'
  ];
  
  const results = queries.map(query => db.query(query).get());
  
  const reportData = {
    queryType: params.queryType,
    database: params.database,
    outputFormat: params.outputFormat,
    executionId: params.executionId,
    userId: params.userId,
    timestamp: new Date().toISOString(),
    metrics: {
      totalUsers: parseInt(results[0].total_users),
      activeUsers: parseInt(results[1].active_users),
      newUsers: parseInt(results[2].new_users)
    }
  };
  
  console.log('✅ Report generated:', reportData.metrics);
  return reportData;
}

// Aggregation job implementation
async function executeAggregationJob(db: Database, params: AnalyticsJobParams): Promise<any> {
  console.log(`🔄 Executing aggregation job for database: ${params.database}`);
  
  // Simulate data aggregation - SQLite compatible
  const aggregationQuery = `
    SELECT 
      date(created_at) as date,
      COUNT(*) as daily_count,
      AVG(julianday(updated_at) - julianday(created_at)) * 86400 as avg_session_duration
    FROM sessions
    WHERE created_at >= datetime('now', '-30 days')
    GROUP BY date(created_at)
    ORDER BY date DESC
  `;
  
  const result = db.query(aggregationQuery).all();
  
  const aggregationData = {
    queryType: params.queryType,
    database: params.database,
    outputFormat: params.outputFormat,
    parallelism: params.parallelism,
    executionId: params.executionId,
    userId: params.userId,
    timestamp: new Date().toISOString(),
    aggregatedData: result
  };
  
  console.log(`✅ Aggregation completed: ${result.length} daily records`);
  return aggregationData;
}

// Send notification
async function sendNotification(params: AnalyticsJobParams, result: any): Promise<void> {
  try {
    const notification = {
      executionId: params.executionId,
      userId: params.userId,
      queryType: params.queryType,
      status: 'completed',
      timestamp: new Date().toISOString(),
      result: {
        recordCount: result.recordCount || result.metrics || result.aggregatedData?.length || 0
      }
    };
    
    // Simulate API call
    console.log('📧 Sending notification:', notification);
    console.log('✅ Notification sent successfully');
    
  } catch (error) {
    console.error('❌ Failed to send notification:', error);
  }
}

// Main execution
async function main() {
  try {
    const params = parseArgs();
    
    // Validate required parameters
    if (!params.queryType) {
      throw new Error('--query-type is required');
    }
    if (!params.database) {
      throw new Error('--database is required');
    }
    
    const result = await executeAnalyticsJob(params);
    
    // Output result based on format
    if (params.outputFormat === 'json') {
      console.log('\n📄 Job Result (JSON):');
      console.log(JSON.stringify(result, null, 2));
    } else {
      console.log('\n📄 Job Result:');
      console.log(result);
    }
    
    console.log('\n🎉 Analytics job completed successfully!');
    
  } catch (error) {
    console.error('❌ Analytics job failed:', error);
    process.exit(1);
  }
}

// Handle different commands
const command = process.argv[2];

switch (command) {
  case 'analytics':
    main();
    break;
  case 'health':
    console.log('✅ Analytics container is healthy');
    break;
  case 'version':
    console.log('synehq/analytics:latest v1.0.0');
    break;
  default:
    console.log('Available commands:');
    console.log('  analytics - Run analytics job');
    console.log('  health    - Health check');
    console.log('  version   - Show version');
    process.exit(1);
}
