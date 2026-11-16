'use strict';
const mysql = require('mysql2/promise');
const store = require('../mysql-store');
let connection;
const pool = { getConnection: () => Promise.resolve(connection) };
beforeAll(() => { vi.spyOn(mysql, 'createPool').mockReturnValue(pool); });
afterAll(() => { vi.restoreAllMocks(); });
beforeEach(() => {
    connection = {
        beginTransaction: vi.fn().mockResolvedValue(), commit: vi.fn().mockResolvedValue(),
        rollback: vi.fn().mockResolvedValue(), release: vi.fn(),
        query: vi.fn().mockResolvedValueOnce([[{ used_at: null, is_active: 1, type: '自助' }]]).mockResolvedValueOnce([[]]),
        execute: vi.fn().mockResolvedValue([{ affectedRows: 1 }])
    };
});
const details = { tokenPreview: 'test', sessionPayload: '{}', cdkCode: 'code', status: 'running', progress: 3 };
it('commits the code reservation and task in one transaction', async () => {
    const result = await store.createActivationTask(details);
    expect(result.jobKey).toBeTruthy();
    expect(connection.execute).toHaveBeenCalledTimes(2);
    expect(connection.commit).toHaveBeenCalledOnce();
    expect(connection.rollback).not.toHaveBeenCalled();
    expect(connection.release).toHaveBeenCalledOnce();
});
it('rolls back the code reservation when task insertion fails', async () => {
    connection.execute.mockResolvedValueOnce([{ affectedRows: 1 }]).mockRejectedValueOnce(new Error('disk full'));
    await expect(store.createActivationTask(details)).rejects.toThrow('disk full');
    expect(connection.rollback).toHaveBeenCalledOnce();
    expect(connection.commit).not.toHaveBeenCalled();
    expect(connection.release).toHaveBeenCalledOnce();
});
it('returns the existing job after locking the code row', async () => {
    connection.query.mockReset().mockResolvedValueOnce([[{ used_at: 'now', is_active: 1, type: '自助' }]])
        .mockResolvedValueOnce([[{ job_key: 'existing' }]]);
    expect(await store.createActivationTask(details)).toEqual({ jobKey: 'existing', existing: true });
    expect(connection.execute).not.toHaveBeenCalled();
});
it('does not consume an already used code', async () => {
    connection.query.mockReset().mockResolvedValueOnce([[{ used_at: 'now', is_active: 1, type: '自助' }]]).mockResolvedValueOnce([[]]);
    expect(await store.createActivationTask(details)).toBeNull();
    expect(connection.execute).not.toHaveBeenCalled();
});
